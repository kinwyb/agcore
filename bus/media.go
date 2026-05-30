package bus

import (
	"github.com/cloudwego/eino/schema"
)

// mediaTypeMapping maps bus media types to schema chat message part types
var mediaTypeMapping = map[string]schema.ChatMessagePartType{
	"image":    schema.ChatMessagePartTypeImageURL,
	"video":    schema.ChatMessagePartTypeVideoURL,
	"audio":    schema.ChatMessagePartTypeAudioURL,
	"document": schema.ChatMessagePartTypeFileURL,
}

// schemaPartTypeToMediaType maps schema part types back to bus media types
var schemaPartTypeToMediaType = map[schema.ChatMessagePartType]string{
	schema.ChatMessagePartTypeImageURL: "image",
	schema.ChatMessagePartTypeVideoURL: "video",
	schema.ChatMessagePartTypeAudioURL: "audio",
	schema.ChatMessagePartTypeFileURL:  "document",
}

// convertMediaToMessageParts converts bus.Media slice to schema.MessageInputPart slice
func convertMediaToMessageParts(media []Media) []schema.MessageInputPart {
	if len(media) == 0 {
		return nil
	}

	parts := make([]schema.MessageInputPart, 0, len(media))
	for _, m := range media {
		part := convertMediaToMessagePart(m)
		if part.Type != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// convertMediaToMessagePart converts a single bus.Media to schema.MessageInputPart
func convertMediaToMessagePart(m Media) schema.MessageInputPart {
	partType, ok := mediaTypeMapping[m.Type]
	if !ok {
		return schema.MessageInputPart{}
	}

	switch partType {
	case schema.ChatMessagePartTypeImageURL:
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					URL:        strPtrIfNotEmpty(m.URL),
					Base64Data: strPtrIfNotEmpty(m.Base64),
					MIMEType:   m.MimeType,
				},
			},
			Extra: m.Metadata,
		}
	case schema.ChatMessagePartTypeAudioURL:
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeAudioURL,
			Audio: &schema.MessageInputAudio{
				MessagePartCommon: schema.MessagePartCommon{
					URL:        strPtrIfNotEmpty(m.URL),
					Base64Data: strPtrIfNotEmpty(m.Base64),
					MIMEType:   m.MimeType,
				},
			},
			Extra: m.Metadata,
		}
	case schema.ChatMessagePartTypeVideoURL:
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeVideoURL,
			Video: &schema.MessageInputVideo{
				MessagePartCommon: schema.MessagePartCommon{
					URL:        strPtrIfNotEmpty(m.URL),
					Base64Data: strPtrIfNotEmpty(m.Base64),
					MIMEType:   m.MimeType,
				},
			},
			Extra: m.Metadata,
		}
	case schema.ChatMessagePartTypeFileURL:
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeFileURL,
			File: &schema.MessageInputFile{
				MessagePartCommon: schema.MessagePartCommon{
					URL:        strPtrIfNotEmpty(m.URL),
					Base64Data: strPtrIfNotEmpty(m.Base64),
					MIMEType:   m.MimeType,
				},
			},
			Extra: m.Metadata,
		}
	default:
		return schema.MessageInputPart{}
	}
}

// strPtrIfNotEmpty returns a pointer to the string if it's not empty, otherwise nil
func strPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// strValOrEmpty returns the string value from pointer, or empty string if nil
func strValOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// buildUserMessage builds a user message with optional media content
func buildUserMessage(content string, media []Media) *schema.Message {
	mediaParts := convertMediaToMessageParts(media)

	// If no media, return simple text message
	if len(mediaParts) == 0 {
		return schema.UserMessage(content)
	}

	// Build multi-content message with text and media
	parts := make([]schema.MessageInputPart, 0, len(mediaParts)+1)

	// Add text part first if content is not empty
	if content != "" {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: content,
		})
	}

	// Add media parts
	parts = append(parts, mediaParts...)

	return &schema.Message{
		Role:                  schema.User,
		Content:               content,
		UserInputMultiContent: parts,
	}
}

// convertOutputPartsToMedia converts schema.MessageOutputPart slice to bus.Media slice
func convertOutputPartsToMedia(parts []schema.MessageOutputPart) []Media {
	if len(parts) == 0 {
		return nil
	}

	media := make([]Media, 0, len(parts))
	for _, part := range parts {
		m := convertOutputPartToMedia(part)
		if m.Type != "" {
			media = append(media, m)
		}
	}
	return media
}

// convertOutputPartToMedia converts a single schema.MessageOutputPart to bus.Media
func convertOutputPartToMedia(part schema.MessageOutputPart) Media {
	mediaType, ok := schemaPartTypeToMediaType[part.Type]
	if !ok {
		return Media{}
	}

	switch part.Type {
	case schema.ChatMessagePartTypeImageURL:
		if part.Image == nil {
			return Media{}
		}
		return Media{
			Type:     mediaType,
			URL:      strValOrEmpty(part.Image.URL),
			Base64:   strValOrEmpty(part.Image.Base64Data),
			MimeType: part.Image.MIMEType,
			Metadata: part.Extra,
		}
	case schema.ChatMessagePartTypeAudioURL:
		if part.Audio == nil {
			return Media{}
		}
		return Media{
			Type:     mediaType,
			URL:      strValOrEmpty(part.Audio.URL),
			Base64:   strValOrEmpty(part.Audio.Base64Data),
			MimeType: part.Audio.MIMEType,
			Metadata: part.Extra,
		}
	case schema.ChatMessagePartTypeVideoURL:
		if part.Video == nil {
			return Media{}
		}
		return Media{
			Type:     mediaType,
			URL:      strValOrEmpty(part.Video.URL),
			Base64:   strValOrEmpty(part.Video.Base64Data),
			MimeType: part.Video.MIMEType,
			Metadata: part.Extra,
		}
	default:
		return Media{}
	}
}

// ExtractMediaFromMessage extracts media from a schema.Message (for assistant responses)
func ExtractMediaFromMessage(msg *schema.Message) []Media {
	if msg == nil {
		return nil
	}

	// Check AssistantGenMultiContent for model-generated multimedia
	if len(msg.AssistantGenMultiContent) > 0 {
		return convertOutputPartsToMedia(msg.AssistantGenMultiContent)
	}

	return nil
}
