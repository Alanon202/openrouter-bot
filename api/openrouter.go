package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"openrouter-bot/config"
	"openrouter-bot/lang"
	"openrouter-bot/user"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/sashabaranov/go-openai"
)

type Model struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Pricing     struct {
		Prompt string `json:"prompt"`
	} `json:"pricing"`
}

type APIResponse struct {
	Data []Model `json:"data"`
}

func GetFreeModels(conf *config.Config) (string, error) {
	resp, err := http.Get(conf.OpenAIBaseURL + "/models")
	if err != nil {
		return "", fmt.Errorf("error get models: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error read response: %v", err)
	}

	var apiResponse APIResponse
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return "", fmt.Errorf("error parse json: %v", err)
	}

	var result strings.Builder
	for _, model := range apiResponse.Data {
		if model.Pricing.Prompt == "0" {
			result.WriteString(fmt.Sprintf("➡ `%s`\n", model.ID))
		}
	}
	return result.String(), nil
}

// ensureClosedTags is a naive helper to close open HTML tags for streaming visualization
func ensureClosedTags(text string) string {
	// We only care about visual stability during the stream.
	// If the AI opens a tag, we want to close it for the current frame.
	
	// Check for unclosed <pre>
	if strings.Count(text, "<pre>") > strings.Count(text, "</pre>") {
		text += "</pre>"
	}
	// Check for unclosed <code>
	if strings.Count(text, "<code>") > strings.Count(text, "</code>") {
		text += "</code>"
	}
	// Check for unclosed <b>
	if strings.Count(text, "<b>") > strings.Count(text, "</b>") {
		text += "</b>"
	}
	// Check for unclosed <i>
	if strings.Count(text, "<i>") > strings.Count(text, "</i>") {
		text += "</i>"
	}
	
	return text
}

// safeEdit attempts to edit a message with HTML, falling back to plain text ONLY if the API rejects it
func safeEdit(ctx context.Context, b *bot.Bot, chatID any, msgID int, text string, parseMode models.ParseMode) {
	if text == "" {
		return
	}

	// For streaming, we try to close tags so the UI doesn't break
	targetText := text
	if parseMode == models.ParseModeHTML {
		targetText = ensureClosedTags(text)
	}

	params := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      targetText,
		ParseMode: parseMode,
	}

	_, err := b.EditMessageText(ctx, params)
	if err != nil {
		// If it's just a "not modified" error, we ignore it completely
		if strings.Contains(err.Error(), "message is not modified") {
			return
		}

		// If HTML formatting fails (e.g. invalid nested tags from AI), fallback to plain text
		if parseMode != "" {
			_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:    chatID,
				MessageID: msgID,
				Text:      text, // Use the raw original text (with visible tags, better than nothing)
			})
		}

		if err != nil {
			log.Printf("Failed to edit message %d: %v", msgID, err)
		}
	}
}

func HandleChatGPTStreamResponse(b *bot.Bot, client *openai.Client, message *models.Message, conf *config.Config, user *user.UsageTracker, threadID int) string {
	ctx := context.Background()
	user.CheckHistory(conf.MaxHistorySize, conf.MaxHistoryTime)
	user.SetLastMessageTime(time.Now())

	if message.Text == "" && len(message.Photo) == 0 {
		return ""
	}

	loadMessage := lang.Translate("loadText", conf.Lang)
	errorMessage := lang.Translate("errorText", conf.Lang)

	sentMsg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		MessageThreadID: threadID,
		Text:            loadMessage,
	})
	if err != nil {
		log.Printf("Failed to send processing message: %v", err)
		return ""
	}
	lastMessageID := sentMsg.ID

	animCtx, animCancel := context.WithCancel(ctx)
	defer animCancel()

	go func() {
		dots := []string{"", ".", "..", "...", "..", "."}
		i := 0
		for {
			select {
			case <-animCtx.Done():
				return
			default:
				text := fmt.Sprintf("%s%s", loadMessage, dots[i])
				safeEdit(ctx, b, message.Chat.ID, lastMessageID, text, "")
				i = (i + 1) % len(dots)
				time.Sleep(400 * time.Millisecond)
			}
		}
	}()

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: user.GetSystemPrompt(),
		},
	}

	for _, msg := range user.GetMessages() {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	if conf.Vision == "true" {
		messages = append(messages, addVisionMessage(b, message, conf))
	} else {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: message.Text,
		})
	}

	req := openai.ChatCompletionRequest{
		Model:            conf.Model.ModelName,
		FrequencyPenalty: float32(conf.Model.FrequencyPenalty),
		PresencePenalty:  float32(conf.Model.PresencePenalty),
		Temperature:      float32(conf.Model.Temperature),
		TopP:             float32(conf.Model.TopP),
		MaxTokens:        conf.MaxTokens,
		Messages:         messages,
		Stream:           true,
	}

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	stream, err := client.CreateChatCompletionStream(streamCtx, req)
	if err != nil {
		animCancel()
		log.Printf("ChatCompletionStream error: %v", err)
		safeEdit(ctx, b, message.Chat.ID, lastMessageID, errorMessage, "")
		return ""
	}
	defer stream.Close()
	user.SetCurrentStream(stream)

	animCancel()
	var messageText string
	responseID := ""
	log.Printf("Stream response started for UserID: %s", user.UserID)

	var lastEditTime time.Time 

	for {
		response, err := stream.Recv()
		if responseID == "" && response.ID != "" {
			responseID = response.ID
		}
		if errors.Is(err, io.EOF) {
			log.Printf("Stream finished, UserID: %s, response ID: %s", user.UserID, responseID)
			user.AddMessage(openai.ChatMessageRoleUser, message.Text)
			user.AddMessage(openai.ChatMessageRoleAssistant, messageText)
			safeEdit(ctx, b, message.Chat.ID, lastMessageID, messageText, models.ParseModeHTML)
			user.SetCurrentStream(nil)
			return responseID
		}

		if err != nil {
			log.Printf("Stream error for UserID: %s: %v", user.UserID, err)
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            err.Error(),
				ParseMode:       models.ParseModeHTML,
				MessageThreadID: threadID,
			})
			user.SetCurrentStream(nil)
			return responseID
		}

		if len(response.Choices) > 0 {
			messageText += response.Choices[0].Delta.Content

			if messageText != "" && time.Since(lastEditTime) > 800*time.Millisecond {
				safeEdit(ctx, b, message.Chat.ID, lastMessageID, messageText, models.ParseModeHTML)
				lastEditTime = time.Now()
			}
		}
	}
}

func addVisionMessage(b *bot.Bot, message *models.Message, config *config.Config) openai.ChatCompletionMessage {
	if len(message.Photo) > 0 {
		photoSize := message.Photo[len(message.Photo)-1]
		fileID := photoSize.FileID
		file, err := b.GetFile(context.Background(), &bot.GetFileParams{
			FileID: fileID,
		})
		if err != nil {
			log.Printf("Error getting file: %v", err)
			return openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: message.Text,
			}
		}
		fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.Token, file.FilePath)
		text := message.Text
		if text == "" {
			text = config.VisionPrompt
		}
		return openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser,
			MultiContent: []openai.ChatMessagePart{
				{
					Type: openai.ChatMessagePartTypeText,
					Text: text,
				},
				{
					Type: openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{
						URL:    fileURL,
						Detail: openai.ImageURLDetail(config.VisionDetails),
					},
				},
			},
		}
	}
	return openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message.Text,
	}
}
