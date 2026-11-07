package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type streamInterceptRequest struct {
	RequestID    string
	SourceFormat string
	ChunkIndex   int
	Body         []byte
}

type streamInterceptResponse struct {
	Body      []byte
	DropChunk bool
}

type claudeStreamBlock struct {
	kind   string
	index  int
	output int
	frames []map[string]json.RawMessage
	text   strings.Builder
	sealed bool
}

type claudeStreamState struct {
	pending  []byte
	frame    []byte
	active   *claudeStreamBlock
	segment  []*claudeStreamBlock
	next     int
	preserve bool
	signed   bool
}

var claudeStreams = struct {
	sync.Mutex
	states map[string]*claudeStreamState
}{states: make(map[string]*claudeStreamState)}

func handleStreamIntercept(request []byte) ([]byte, error) {
	var req streamInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, fmt.Errorf("decode stream intercept: %w", err)
	}
	if req.SourceFormat != "claude" || req.ChunkIndex == -1 {
		return okEnvelope(streamInterceptResponse{Body: req.Body})
	}
	body, err := normalizeClaudeStream(req.RequestID, req.Body)
	if err != nil {
		return nil, err
	}
	return okEnvelope(streamInterceptResponse{Body: body, DropChunk: len(body) == 0})
}

func handleStreamCompletion(request []byte) ([]byte, error) {
	var req struct{ RequestID string }
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, fmt.Errorf("decode request completion: %w", err)
	}
	cleanupClaudeStream(req.RequestID)
	return okEnvelope(struct{}{})
}

func cleanupClaudeStream(requestID string) {
	claudeStreams.Lock()
	defer claudeStreams.Unlock()
	delete(claudeStreams.states, requestID)
}

func normalizeClaudeStream(requestID string, body []byte) ([]byte, error) {
	if requestID == "" {
		return nil, fmt.Errorf("Claude stream normalization requires a request ID")
	}
	claudeStreams.Lock()
	defer claudeStreams.Unlock()
	state, exists := claudeStreams.states[requestID]
	if !exists {
		state = &claudeStreamState{}
		claudeStreams.states[requestID] = state
	}
	state.pending = append(state.pending, body...)
	var out bytes.Buffer
	for {
		end := bytes.IndexByte(state.pending, '\n')
		if end < 0 {
			break
		}
		line := bytes.TrimSuffix(state.pending[:end], []byte("\r"))
		if len(line) == 0 {
			if err := state.processFrame(state.frame, &out); err != nil {
				delete(claudeStreams.states, requestID)
				return nil, fmt.Errorf("normalize Claude stream %s: %w", requestID, err)
			}
			state.frame = nil
		} else {
			state.frame = append(state.frame, line...)
			state.frame = append(state.frame, '\n')
		}
		state.pending = state.pending[end+1:]
	}
	if len(state.pending) == 0 {
		state.pending = nil
	}
	return out.Bytes(), nil
}

func (s *claudeStreamState) processFrame(frame []byte, out *bytes.Buffer) error {
	var data [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, bytes.TrimPrefix(line[5:], []byte(" ")))
		}
	}
	if len(data) == 0 {
		if len(frame) > 0 {
			out.Write(frame)
			out.WriteByte('\n')
		}
		return nil
	}
	payload := bytes.Join(data, []byte("\n"))
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode SSE data: %w", err)
	}
	var fields struct {
		Type         string
		Index        int
		ContentBlock struct {
			Type      string
			Text      string
			Signature string
			Citations []json.RawMessage
		} `json:"content_block"`
		Delta struct {
			Type string
			Text string
		}
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fmt.Errorf("decode Claude event: %w", err)
	}
	switch fields.Type {
	case "content_block_start":
		if s.active != nil {
			return fmt.Errorf("content block %d started before previous block stopped", fields.Index)
		}
		kind := fields.ContentBlock.Type
		buffered := !s.signed && (kind == "text" || (kind == "thinking" && len(s.segment) > 0))
		if fields.ContentBlock.Signature != "" {
			s.signed = true
		}
		if !buffered {
			if err := s.flush(out); err != nil {
				return err
			}
		}
		block := &claudeStreamBlock{kind: kind, index: fields.Index}
		s.active = block
		if buffered {
			block.frames = append(block.frames, event)
			block.text.WriteString(fields.ContentBlock.Text)
			s.segment = append(s.segment, block)
			// Signed thinking and annotated text must retain their original block boundaries.
			s.preserve = s.preserve || fields.ContentBlock.Signature != "" || len(fields.ContentBlock.Citations) > 0
			return nil
		}
		block.output = s.next
		s.next++
		return emitClaudeStreamEvent(out, event, block.output)
	case "content_block_delta", "content_block_stop":
		if s.active == nil {
			return fmt.Errorf("%s received without an active block", fields.Type)
		}
		block := s.active
		if fields.Index != block.index {
			return fmt.Errorf("content block index %d does not match active block %d", fields.Index, block.index)
		}
		if fields.Delta.Type == "signature_delta" {
			s.signed = true
		}
		buffered := len(block.frames) > 0
		if buffered {
			block.frames = append(block.frames, event)
			if fields.Type == "content_block_delta" {
				if block.kind == "text" && fields.Delta.Type == "text_delta" {
					block.text.WriteString(fields.Delta.Text)
				} else if block.kind == "text" || fields.Delta.Type == "signature_delta" {
					s.preserve = true
				}
			}
		} else if err := emitClaudeStreamEvent(out, event, block.output); err != nil {
			return err
		}
		if fields.Type == "content_block_stop" {
			block.sealed = true
			s.active = nil
		}
		return nil
	case "message_delta", "message_stop", "error":
		if s.active != nil && fields.Type != "error" {
			return fmt.Errorf("%s received before content block stopped", fields.Type)
		}
		if err := s.flush(out); err != nil {
			return err
		}
	}
	out.Write(frame)
	out.WriteByte('\n')
	return nil
}

func (s *claudeStreamState) flush(out *bytes.Buffer) error {
	if len(s.segment) == 0 {
		return nil
	}
	for _, block := range s.segment {
		if !block.sealed {
			s.preserve = true
		}
	}
	if s.preserve {
		for _, block := range s.segment {
			for _, event := range block.frames {
				if err := emitClaudeStreamEvent(out, event, s.next); err != nil {
					return err
				}
			}
			s.next++
		}
	} else {
		var text strings.Builder
		var firstText map[string]json.RawMessage
		for _, block := range s.segment {
			if block.kind == "text" {
				if firstText == nil {
					firstText = block.frames[0]
				}
				text.WriteString(block.text.String())
				continue
			}
			for _, event := range block.frames {
				if err := emitClaudeStreamEvent(out, event, s.next); err != nil {
					return err
				}
			}
			s.next++
		}
		var content map[string]json.RawMessage
		if err := json.Unmarshal(firstText["content_block"], &content); err != nil {
			return fmt.Errorf("decode text block: %w", err)
		}
		content["text"] = json.RawMessage(`""`)
		encoded, err := json.Marshal(content)
		if err != nil {
			return err
		}
		firstText["content_block"] = encoded
		if err := emitClaudeStreamEvent(out, firstText, s.next); err != nil {
			return err
		}
		delta, err := json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text_delta", Text: text.String()})
		if err != nil {
			return err
		}
		if err := emitClaudeStreamEvent(out, map[string]json.RawMessage{"type": json.RawMessage(`"content_block_delta"`), "delta": delta}, s.next); err != nil {
			return err
		}
		if err := emitClaudeStreamEvent(out, map[string]json.RawMessage{"type": json.RawMessage(`"content_block_stop"`)}, s.next); err != nil {
			return err
		}
		s.next++
	}
	s.segment = nil
	s.preserve = false
	return nil
}

func emitClaudeStreamEvent(out *bytes.Buffer, event map[string]json.RawMessage, index int) error {
	event["index"] = json.RawMessage(fmt.Sprintf("%d", index))
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode Claude event: %w", err)
	}
	var kind string
	if err := json.Unmarshal(event["type"], &kind); err != nil {
		return fmt.Errorf("decode Claude event type: %w", err)
	}
	out.WriteString("event: ")
	out.WriteString(kind)
	out.WriteString("\ndata: ")
	out.Write(payload)
	out.WriteString("\n\n")
	return nil
}
