package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// imageEditMultipartMemory is memory byte limit retained for image edit forms, spilling excess to temp files
const imageEditMultipartMemory = 32 << 20

// imageEditMaskInstruction describes mask instructions (white edits, black preserves)
const imageEditMaskInstruction = "The previous image is an edit mask for the first image: white pixels mark the area to edit and black pixels mark the area to keep. Apply the following edit only inside the white area and keep everything else unchanged."

// handleOpenAIImageEdits processes multipart image/mask edits according to OpenAI Images parameters
func (s *server) handleOpenAIImageEdits(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(imageEditMultipartMemory); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	request := openAIImageRequest{
		Model: r.FormValue("model"), Prompt: r.FormValue("prompt"), Size: r.FormValue("size"),
		Quality: r.FormValue("quality"), ResponseFormat: r.FormValue("response_format"),
	}
	request.Stream, _ = strconv.ParseBool(r.FormValue("stream"))
	if value := strings.TrimSpace(r.FormValue("n")); value != "" {
		count, err := strconv.Atoi(value)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "n must be an integer")
			return
		}
		request.N = count
	}
	var inputs []aistudio.Part
	for _, header := range append(r.MultipartForm.File["image"], r.MultipartForm.File["image[]"]...) {
		data, err := readMultipartFile(header)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		mimeType := declaredMediaType(header.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		mimeType, data = normalizeImagePayload(mimeType, data)
		inputs = append(inputs, aistudio.Part{InlineData: &aistudio.Blob{MIME: mimeType, Data: data}})
	}
	if len(inputs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "image is required")
		return
	}
	if masks := r.MultipartForm.File["mask"]; len(masks) > 0 {
		mask, err := imageEditMask(masks[0])
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		inputs = append(inputs, aistudio.Part{InlineData: &aistudio.Blob{MIME: "image/png", Data: mask}}, aistudio.Part{Text: imageEditMaskInstruction})
	}
	s.writeOpenAIImages(w, r, request, inputs, "image_edit")
}

// readMultipartFile reads entire content of multipart file part
func readMultipartFile(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// imageEditMask converts transparent pixels to white and other pixels to black, returning grayscale PNG
func imageEditMask(header *multipart.FileHeader) ([]byte, error) {
	data, err := readMultipartFile(header)
	if err != nil {
		return nil, err
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mask must be a PNG image: %w", err)
	}
	bounds := source.Bounds()
	mask := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := source.At(x, y).RGBA(); alpha == 0 {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, mask); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
