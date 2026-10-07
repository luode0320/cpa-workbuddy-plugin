package cursorproto

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_ImageWriteExecutor_reuses_success_when_identical_write_is_repeated(t *testing.T) {
	// Given
	project := t.TempDir()
	target := filepath.Join(project, "assets", "image-2.jpg")
	data := []byte("\xff\xd8\xffimage")
	executor := NewImageWriteExecutor(project)
	first, handled, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 9, "first", target, data))
	require.NoError(t, err)
	require.True(t, handled)

	// When
	reply, handled, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 10, "repeat", target, data))

	// Then
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, imageReplyPath(t, first), imageReplyPath(t, reply))
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	exec := client.Get(field(client, "exec_client_message")).Message()
	require.EqualValues(t, 10, exec.Get(field(exec, "id")).Uint())
	require.Equal(t, "repeat", exec.Get(field(exec, "exec_id")).String())
	entries, err := os.ReadDir(filepath.Dir(target))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, executor.Cleanup())
	require.NoError(t, executor.Cleanup())
}

func Test_ImageWriteExecutor_isolates_concurrent_requests_with_same_filename(t *testing.T) {
	// Given
	project := t.TempDir()
	target := filepath.Join(project, "assets", "image-2.jpg")
	data := []byte("\xff\xd8\xffimage")
	raw := encodeImageWriteRequest(t, 9, "image", target, data)
	executors := []*ImageWriteExecutor{NewImageWriteExecutor(project), NewImageWriteExecutor(project)}
	type result struct {
		index int
		reply []byte
		err   error
	}
	results := make(chan result, len(executors))
	start := make(chan struct{})

	// When
	for index, executor := range executors {
		go func() {
			<-start
			reply, _, err := executor.HandleServerMessage(raw)
			results <- result{index: index, reply: reply, err: err}
		}()
	}
	close(start)
	paths := make([]string, len(executors))
	for range executors {
		result := <-results
		require.NoError(t, result.err)
		paths[result.index] = imageReplyPath(t, result.reply)
	}

	// Then
	require.NotEqual(t, paths[0], paths[1])
	for _, path := range paths {
		require.Equal(t, filepath.Dir(target), filepath.Dir(path))
		require.Equal(t, ".jpg", filepath.Ext(path))
		written, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, data, written)
	}
	require.NoError(t, executors[0].Cleanup())
	_, err := os.Stat(paths[0])
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(paths[1])
	require.NoError(t, err)
	require.NoError(t, executors[1].Cleanup())
}

func Test_ImageWriteExecutor_preserves_existing_file_and_symlink_when_name_collides(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			// Given
			project := t.TempDir()
			target := filepath.Join(project, "assets", "image-2.jpg")
			foreign := filepath.Join(project, "foreign.jpg")
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
			require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0o600))
			if symlink {
				symlinkOrSkip(t, foreign, target)
			} else {
				require.NoError(t, os.WriteFile(target, []byte("foreign"), 0o600))
			}
			executor := NewImageWriteExecutor(project)

			// When
			reply, _, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 9, "image", target, []byte("\xff\xd8\xffimage")))

			// Then
			require.NoError(t, err)
			require.NotEqual(t, target, imageReplyPath(t, reply))
			require.NoError(t, executor.Cleanup())
			for _, path := range []string{target, foreign} {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "foreign", string(data))
			}
		})
	}
}

func Test_ImageWriteExecutor_keeps_distinct_content_when_filename_is_reused(t *testing.T) {
	// Given
	project := t.TempDir()
	target := filepath.Join(project, "assets", "image-2.jpg")
	executor := NewImageWriteExecutor(project)
	first := []byte("\xff\xd8\xfffirst")
	second := []byte("\xff\xd8\xffsecond")

	// When
	paths := make([]string, 0, 3)
	for _, data := range [][]byte{first, second, first} {
		reply, _, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 9, "image", target, data))
		require.NoError(t, err)
		paths = append(paths, imageReplyPath(t, reply))
	}

	// Then
	require.NotEqual(t, paths[0], paths[1])
	require.Equal(t, paths[0], paths[2])
	for index, expected := range [][]byte{first, second} {
		actual, err := os.ReadFile(paths[index])
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
	require.NoError(t, executor.Cleanup())
	entries, err := os.ReadDir(filepath.Dir(target))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func imageReplyPath(t *testing.T, reply []byte) string {
	t.Helper()
	client, err := newMessage("AgentClientMessage")
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(reply, client))
	exec := client.Get(field(client, "exec_client_message")).Message()
	result := exec.Get(field(exec, "write_result")).Message()
	success := result.Get(field(result, "success")).Message()
	return success.Get(field(success, "path")).String()
}

// symlinkOrSkip 创建符号链接；在 Windows 未开启开发者模式/无特权时
// os.Symlink 会返回权限错误，此时跳过该用例（Linux CI 仍完整覆盖）。
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on Windows without privileges: %v", err)
		}
		require.NoError(t, err)
	}
}

func Test_ImageWriteExecutor_rejects_unsafe_writes_before_collision_fallback(t *testing.T) {
	for _, scenario := range []string{"outside", "symlink-directory", "oversized", "non-image"} {
		t.Run(scenario, func(t *testing.T) {
			// Given
			project := t.TempDir()
			target := filepath.Join(project, "assets", "image.jpg")
			data := []byte("\xff\xd8\xffimage")
			message := "project assets directory"
			switch scenario {
			case "outside":
				target = filepath.Join(project, "image.jpg")
			case "symlink-directory":
				symlinkOrSkip(t, t.TempDir(), filepath.Dir(target))
				message = "not a real directory"
			case "oversized":
				data = make([]byte, maxGeneratedImageBytes+1)
				message = "exceeds 16 MiB"
			case "non-image":
				data = []byte("not an image")
				message = "non-image data"
			}
			executor := NewImageWriteExecutor(project)

			// When
			reply, handled, err := executor.HandleServerMessage(encodeImageWriteRequest(t, 9, "image", target, data))

			// Then
			require.ErrorContains(t, err, message)
			require.True(t, handled)
			require.Nil(t, reply)
			require.NoError(t, executor.Cleanup())
			_, err = os.Stat(target)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
