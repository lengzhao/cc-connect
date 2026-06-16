package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/chenhg5/cc-connect/core"
)

// SendImage emits an image artifact with raw bytes.
func (p *Platform) SendImage(_ context.Context, replyCtx any, img core.ImageAttachment) error {
	if len(img.Data) == 0 {
		return fmt.Errorf("a2a: empty image attachment")
	}
	taskID := taskIDFromReplyCtx(replyCtx)
	if taskID == "" {
		return fmt.Errorf("a2a: unsupported send context %T", replyCtx)
	}
	mimeType := strings.TrimSpace(img.MimeType)
	if mimeType == "" {
		mimeType = http.DetectContentType(img.Data)
	}
	if mimeType == "" || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = "image/png"
	}
	name := strings.TrimSpace(img.FileName)
	if name == "" {
		name = "image.png"
	}
	part := rawPartFromAttachment(mimeType, name, img.Data)
	if !p.pushPartArtifact(taskID, sdka2a.ContentParts{part}) {
		return fmt.Errorf("a2a: task %q is not pending", taskID)
	}
	return nil
}

// SendFile emits a file artifact with raw bytes.
func (p *Platform) SendFile(_ context.Context, replyCtx any, file core.FileAttachment) error {
	if len(file.Data) == 0 {
		return fmt.Errorf("a2a: empty file attachment")
	}
	taskID := taskIDFromReplyCtx(replyCtx)
	if taskID == "" {
		return fmt.Errorf("a2a: unsupported send context %T", replyCtx)
	}
	name := strings.TrimSpace(file.FileName)
	if name == "" {
		name = "attachment"
	}
	part := rawPartFromAttachment(file.MimeType, name, file.Data)
	if !p.pushPartArtifact(taskID, sdka2a.ContentParts{part}) {
		return fmt.Errorf("a2a: task %q is not pending", taskID)
	}
	return nil
}

func rawPartFromAttachment(mimeType, fileName string, data []byte) *sdka2a.Part {
	part := sdka2a.NewRawPart(append([]byte(nil), data...))
	part.MediaType = strings.TrimSpace(mimeType)
	part.Filename = strings.TrimSpace(fileName)
	if part.MediaType == "" {
		part.MediaType = http.DetectContentType(data)
	}
	if part.MediaType == "" {
		part.MediaType = "application/octet-stream"
	}
	return part
}

func (p *Platform) pushPartArtifact(taskID string, parts sdka2a.ContentParts) bool {
	if len(parts) == 0 {
		return true
	}
	return p.pushArtifactEvent(taskID, pendingArtifact{parts: parts})
}

func partsToCore(parts sdka2a.ContentParts) (string, []core.ImageAttachment, *core.AudioAttachment, []core.FileAttachment, error) {
	var text []string
	var images []core.ImageAttachment
	var files []core.FileAttachment
	var audio *core.AudioAttachment

	for _, part := range parts {
		if part == nil {
			continue
		}
		if value := strings.TrimSpace(part.Text()); value != "" {
			text = append(text, value)
			continue
		}
		if data := part.Data(); data != nil {
			b, err := json.Marshal(data)
			if err != nil {
				return "", nil, nil, nil, err
			}
			text = append(text, string(b))
			continue
		}
		if raw := part.Raw(); len(raw) > 0 {
			classifyRawAttachment(raw, part.MediaType, part.Filename, &images, &audio, &files)
			continue
		}
		if fileURL := part.URL(); fileURL != "" {
			data, mimeType, fileName, err := downloadURLPart(string(fileURL), part.MediaType, part.Filename)
			if err != nil {
				slog.Warn("a2a: download file URL failed", "url", fileURL, "error", err)
				text = append(text, fmt.Sprintf("File URL: %s", fileURL))
				continue
			}
			classifyRawAttachment(data, mimeType, fileName, &images, &audio, &files)
		}
	}

	return strings.Join(text, "\n\n"), images, audio, files, nil
}

func classifyRawAttachment(
	data []byte,
	mimeType, fileName string,
	images *[]core.ImageAttachment,
	audio **core.AudioAttachment,
	files *[]core.FileAttachment,
) {
	mt := strings.TrimSpace(strings.ToLower(mimeType))
	if mt == "" {
		mt = strings.ToLower(http.DetectContentType(data))
	}
	name := strings.TrimSpace(fileName)

	switch {
	case strings.HasPrefix(mt, "audio/"):
		format := "mp3"
		if parts := strings.SplitN(mt, "/", 2); len(parts) == 2 && parts[1] != "" {
			format = parts[1]
		}
		*audio = &core.AudioAttachment{
			MimeType: mimeType,
			Data:     append([]byte(nil), data...),
			Format:   format,
		}
	case strings.HasPrefix(mt, "image/"):
		*images = append(*images, core.ImageAttachment{
			MimeType: mimeType,
			Data:     append([]byte(nil), data...),
			FileName: name,
		})
	default:
		if mt == "" {
			mt = "application/octet-stream"
		}
		*files = append(*files, core.FileAttachment{
			MimeType: mt,
			Data:     append([]byte(nil), data...),
			FileName: name,
		})
	}
}

func downloadURLPart(fileURL, mimeType, fileName string) ([]byte, string, string, error) {
	if strings.TrimSpace(fileURL) == "" {
		return nil, "", "", fmt.Errorf("empty file URL")
	}
	resp, err := core.HTTPClient.Get(fileURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("download file URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", "", fmt.Errorf("download file URL status %d: %s", resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("read file URL response: %w", err)
	}
	mt := strings.TrimSpace(mimeType)
	if mt == "" {
		mt = strings.TrimSpace(resp.Header.Get("Content-Type"))
		if idx := strings.Index(mt, ";"); idx >= 0 {
			mt = strings.TrimSpace(mt[:idx])
		}
	}
	if mt == "" {
		mt = http.DetectContentType(data)
	}
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = fileNameFromURL(fileURL)
	}
	return data, mt, name, nil
}

func fileNameFromURL(rawURL string) string {
	parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

var _ core.ImageSender = (*Platform)(nil)
var _ core.FileSender = (*Platform)(nil)
