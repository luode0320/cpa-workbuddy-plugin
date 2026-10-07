package cursorproto

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const maxGeneratedImageBytes = 16 << 20

type ImageWriteExecutor struct {
	projectFolder string
	writtenPaths  []string
	completed     map[imageWriteKey]string
}

type imageWriteKey struct {
	path   string
	digest [sha256.Size]byte
}

type imageWriteRequest struct {
	id     uint32
	execID string
	path   string
	data   []byte
}

func NewImageWriteExecutor(projectFolder string) *ImageWriteExecutor {
	return &ImageWriteExecutor{projectFolder: projectFolder}
}

func (executor *ImageWriteExecutor) HandleServerMessage(raw []byte) ([]byte, bool, error) {
	request, handled, err := decodeImageWriteRequest(raw)
	if err != nil || !handled {
		return nil, handled, err
	}
	target, err := executor.writeImage(request.path, request.data)
	if err != nil {
		return nil, true, err
	}
	reply, err := encodeImageWriteSuccess(request, target)
	if err != nil {
		return nil, true, err
	}
	return reply, true, nil
}

func (executor *ImageWriteExecutor) Cleanup() error {
	var cleanupErr error
	for index := len(executor.writtenPaths) - 1; index >= 0; index-- {
		path := executor.writtenPaths[index]
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove Cursor generated image %q: %w", path, err))
		}
	}
	executor.writtenPaths = nil
	executor.completed = nil
	return cleanupErr
}

func decodeImageWriteRequest(raw []byte) (imageWriteRequest, bool, error) {
	server, err := newMessage("AgentServerMessage")
	if err != nil {
		return imageWriteRequest{}, false, err
	}
	if err := proto.Unmarshal(raw, server); err != nil {
		return imageWriteRequest{}, false, fmt.Errorf("decode Cursor server message for image write: %w", err)
	}
	active := server.WhichOneof(server.Descriptor().Oneofs().ByName("message"))
	if active == nil || active.Name() != "exec_server_message" {
		return imageWriteRequest{}, false, nil
	}
	execMessage := server.Get(active).Message()
	operation := execMessage.WhichOneof(execMessage.Descriptor().Oneofs().ByName("message"))
	if operation == nil || operation.Name() != "write_args" {
		return imageWriteRequest{}, false, nil
	}
	args := execMessage.Get(operation).Message()
	pathField, err := requireField(args, "path")
	if err != nil {
		return imageWriteRequest{}, true, err
	}
	textField, err := requireField(args, "file_text")
	if err != nil {
		return imageWriteRequest{}, true, err
	}
	dataField, err := requireField(args, "file_bytes")
	if err != nil {
		return imageWriteRequest{}, true, err
	}
	path := args.Get(pathField).String()
	data := args.Get(dataField).Bytes()
	if path == "" {
		return imageWriteRequest{}, true, errors.New("Cursor image write requires a path")
	}
	if args.Get(textField).String() != "" || len(data) == 0 {
		return imageWriteRequest{}, true, errors.New("Cursor image write requires binary file bytes")
	}
	return imageWriteRequest{
		id:     uint32(execMessage.Get(field(execMessage, "id")).Uint()),
		execID: execMessage.Get(field(execMessage, "exec_id")).String(),
		path:   path,
		data:   append([]byte(nil), data...),
	}, true, nil
}

func (executor *ImageWriteExecutor) writeImage(requestedPath string, data []byte) (string, error) {
	if len(data) > maxGeneratedImageBytes {
		return "", fmt.Errorf("Cursor generated image exceeds %d MiB", maxGeneratedImageBytes>>20)
	}
	if mediaType := http.DetectContentType(data); !strings.HasPrefix(mediaType, "image/") {
		return "", fmt.Errorf("Cursor image write returned non-image data (%s)", mediaType)
	}
	projectFolder, err := filepath.Abs(executor.projectFolder)
	if err != nil {
		return "", fmt.Errorf("resolve Cursor project folder: %w", err)
	}
	assetsDirectory := filepath.Join(projectFolder, "assets")
	target := filepath.Clean(requestedPath)
	if !filepath.IsAbs(target) || filepath.Dir(target) != assetsDirectory {
		return "", fmt.Errorf("Cursor image write path must be a direct child of the project assets directory")
	}
	if info, statErr := os.Lstat(assetsDirectory); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("Cursor project assets path is not a real directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Cursor project assets directory: %w", statErr)
	}
	if err := os.MkdirAll(assetsDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create Cursor project assets directory: %w", err)
	}
	info, err := os.Lstat(assetsDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect Cursor project assets directory after creation: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("Cursor project assets path is not a real directory")
	}
	key := imageWriteKey{path: target, digest: sha256.Sum256(data)}
	if completed, ok := executor.completed[key]; ok {
		return completed, nil
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = os.CreateTemp(assetsDirectory, "cursor-image-*"+filepath.Ext(target))
		if err == nil {
			target = file.Name()
		}
	}
	if err != nil {
		return "", fmt.Errorf("create Cursor generated image %q: %w", target, err)
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", fmt.Errorf("write Cursor generated image %q: %w", target, errors.Join(err, os.Remove(target)))
	}
	executor.writtenPaths = append(executor.writtenPaths, target)
	if executor.completed == nil {
		executor.completed = make(map[imageWriteKey]string)
	}
	executor.completed[key] = target
	return target, nil
}

func encodeImageWriteSuccess(request imageWriteRequest, path string) ([]byte, error) {
	client, err := newMessage("AgentClientMessage")
	if err != nil {
		return nil, err
	}
	execReply, err := nestedMessage(client, "exec_client_message")
	if err != nil {
		return nil, err
	}
	if err := setUint32(execReply, "id", request.id); err != nil {
		return nil, err
	}
	if request.execID != "" {
		if err := setString(execReply, "exec_id", request.execID); err != nil {
			return nil, err
		}
	}
	result, err := nestedMessage(execReply, "write_result")
	if err != nil {
		return nil, err
	}
	success, err := nestedMessage(result, "success")
	if err != nil {
		return nil, err
	}
	if err := setString(success, "path", path); err != nil {
		return nil, err
	}
	if err := setInt32(success, "lines_created", 0); err != nil {
		return nil, err
	}
	if err := setInt32(success, "file_size", int32(len(request.data))); err != nil {
		return nil, err
	}
	if err := setMessage(result, "success", success); err != nil {
		return nil, err
	}
	if err := setMessage(execReply, "write_result", result); err != nil {
		return nil, err
	}
	if err := setMessage(client, "exec_client_message", execReply); err != nil {
		return nil, err
	}
	reply, err := proto.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("encode Cursor image write success: %w", err)
	}
	return reply, nil
}

func setInt32(message protoreflect.Message, name protoreflect.Name, value int32) error {
	descriptor, err := requireField(message, name)
	if err != nil {
		return err
	}
	if descriptor.Kind() != protoreflect.Int32Kind {
		return fmt.Errorf("Cursor field %q is not int32", name)
	}
	message.Set(descriptor, protoreflect.ValueOfInt32(value))
	return nil
}
