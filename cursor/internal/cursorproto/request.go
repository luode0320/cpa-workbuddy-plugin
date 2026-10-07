package cursorproto

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type ContinuationMode uint8

const (
	FullReplay ContinuationMode = iota
	CheckpointSuffix
	CheckpointResume
)

type RunRequest struct {
	ConversationID string
	MessageID      string
	Model          string
	System         string
	Prompt         string
	TimeZone       string
	WorkspacePaths []string
	ProjectFolder  string
	Tools          []ToolDefinition
	Images         []ImageAttachment
	Attachments    []FileAttachment
	Mode           ContinuationMode
	Checkpoint     []byte
}

type RequestEnvironment struct {
	TimeZone       string
	WorkspacePaths []string
	ProjectFolder  string
	Tools          []ToolDefinition
}

func EncodeRunRequest(request RunRequest) ([]byte, error) {
	if err := validateRunRequest(request); err != nil {
		return nil, err
	}
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	run, err := newMessage("AgentRunRequest")
	if err != nil {
		return nil, err
	}
	if err := populateRunRequest(run, request); err != nil {
		return nil, err
	}
	if err := setMessage(client, "run_request", run); err != nil {
		return nil, err
	}
	raw, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor run request: %w", err)
	}
	return raw, nil
}

func populateRunRequest(run protoreflect.Message, request RunRequest) error {
	state, err := conversationState(run, request)
	if err != nil {
		return err
	}
	action, err := buildAction(run, request)
	if err != nil {
		return err
	}
	model, err := buildModel(run, request.Model)
	if err != nil {
		return err
	}
	if err := setMessage(run, "conversation_state", state); err != nil {
		return err
	}
	if err := setMessage(run, "action", action); err != nil {
		return err
	}
	if err := setMessage(run, "model_details", model); err != nil {
		return err
	}
	if err := setString(run, "conversation_id", request.ConversationID); err != nil {
		return err
	}
	if err := addTools(run, request.Tools); err != nil {
		return err
	}
	return nil
}

func validateRunRequest(request RunRequest) error {
	if request.ConversationID == "" || request.Model == "" {
		return fmt.Errorf("Cursor run request requires conversation and model")
	}
	switch request.Mode {
	case FullReplay:
		if len(request.Checkpoint) > 0 || request.MessageID == "" || request.Prompt == "" {
			return fmt.Errorf("Cursor full replay requires message and prompt without checkpoint")
		}
	case CheckpointSuffix:
		if len(request.Checkpoint) == 0 || request.MessageID == "" || request.Prompt == "" {
			return fmt.Errorf("Cursor checkpoint suffix requires checkpoint, message, and prompt")
		}
	case CheckpointResume:
		if len(request.Checkpoint) == 0 || request.Prompt != "" {
			return fmt.Errorf("Cursor checkpoint resume requires checkpoint without prompt")
		}
	default:
		return fmt.Errorf("Cursor run request has unsupported continuation mode %d", request.Mode)
	}
	return nil
}

func conversationState(run protoreflect.Message, request RunRequest) (protoreflect.Message, error) {
	state, err := nestedMessage(run, "conversation_state")
	if err != nil {
		return nil, err
	}
	if request.Mode == FullReplay {
		return state, nil
	}
	if err := proto.Unmarshal(request.Checkpoint, state.Interface()); err != nil {
		return nil, fmt.Errorf("decode Cursor conversation checkpoint: %w", err)
	}
	return state, nil
}

func buildAction(run protoreflect.Message, request RunRequest) (protoreflect.Message, error) {
	action, err := nestedMessage(run, "action")
	if err != nil {
		return nil, err
	}
	if request.Mode == CheckpointResume {
		resume, err := nestedMessage(action, "resume_action")
		if err != nil {
			return nil, err
		}
		if err := addRequestContext(resume, request); err != nil {
			return nil, err
		}
		if err := setMessage(action, "resume_action", resume); err != nil {
			return nil, err
		}
		return action, nil
	}
	userAction, err := nestedMessage(action, "user_message_action")
	if err != nil {
		return nil, err
	}
	userMessage, err := nestedMessage(userAction, "user_message")
	if err != nil {
		return nil, err
	}
	prompt := request.Prompt
	if request.Mode == FullReplay && request.System != "" {
		prompt = request.System + "\n\n" + prompt
	}
	if err := setString(userMessage, "text", prompt); err != nil {
		return nil, err
	}
	if err := setString(userMessage, "message_id", request.MessageID); err != nil {
		return nil, err
	}
	if err := addSelectedContext(userMessage, request.Images, request.Attachments); err != nil {
		return nil, err
	}
	if err := setMessage(userAction, "user_message", userMessage); err != nil {
		return nil, err
	}
	if err := addRequestContext(userAction, request); err != nil {
		return nil, err
	}
	if err := setMessage(action, "user_message_action", userAction); err != nil {
		return nil, err
	}
	return action, nil
}

func addRequestContext(parent protoreflect.Message, request RunRequest) error {
	requestContext, err := nestedMessage(parent, "request_context")
	if err != nil {
		return err
	}
	if err := populateRequestContext(requestContext, RequestEnvironment{
		TimeZone: request.TimeZone, WorkspacePaths: request.WorkspacePaths, ProjectFolder: request.ProjectFolder,
	}); err != nil {
		return err
	}
	return setMessage(parent, "request_context", requestContext)
}

func populateRequestContext(requestContext protoreflect.Message, request RequestEnvironment) error {
	environment, err := nestedMessage(requestContext, "env")
	if err != nil {
		return err
	}
	if request.TimeZone == "" {
		request.TimeZone = "UTC"
	}
	if err := setString(environment, "time_zone", request.TimeZone); err != nil {
		return err
	}
	workspacePaths, err := requireField(environment, "workspace_paths")
	if err != nil {
		return err
	}
	if !workspacePaths.IsList() || workspacePaths.Kind() != protoreflect.StringKind {
		return fmt.Errorf("Cursor field %q is not a string list", workspacePaths.Name())
	}
	for _, path := range request.WorkspacePaths {
		if path != "" {
			environment.Mutable(workspacePaths).List().Append(protoreflect.ValueOfString(path))
		}
	}
	if request.ProjectFolder != "" {
		if err := setString(environment, "project_folder", request.ProjectFolder); err != nil {
			return err
		}
	}
	if err := setMessage(requestContext, "env", environment); err != nil {
		return err
	}
	return nil
}

func EncodeClientHeartbeat() ([]byte, error) {
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	heartbeat, err := nestedMessage(client, "client_heartbeat")
	if err != nil {
		return nil, err
	}
	if err := setMessage(client, "client_heartbeat", heartbeat); err != nil {
		return nil, err
	}
	raw, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor client heartbeat: %w", err)
	}
	return raw, nil
}

func buildModel(run protoreflect.Message, modelID string) (protoreflect.Message, error) {
	model, err := nestedMessage(run, "model_details")
	if err != nil {
		return nil, err
	}
	for _, name := range []protoreflect.Name{"model_id", "display_model_id", "display_name", "display_name_short"} {
		if err := setString(model, name, modelID); err != nil {
			return nil, err
		}
	}
	return model, nil
}
