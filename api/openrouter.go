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

func HandleChatGPTStreamResponse(b *bot.Bot, client *openai.Client, message *models.Message, conf *config.Config, user *user.UsageTracker, threadID int) string {
	ctx := context.Background()
	user.CheckHistory(conf.MaxHistorySize, conf.MaxHistoryTime)
	user.SetLastMessageTime(time.Now())

	// Send a loading message with animation dots
	loadMessage := lang.Translate("loadText", conf.Lang)
	errorMessage := lang.Translate("errorText", conf.Lang)

	// Send initial processing message
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

	// Goroutine for animation dots
	stopAnimation := make(chan bool, 1) // Buffered to avoid blocking
	go func() {
		dots := []string{"", ".", "..", "...", "..", "."}
		i := 0
		for {
			select {
			case <-stopAnimation:
				return
			default:
				text := fmt.Sprintf("%s%s", loadMessage, dots[i])
				_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
					ChatID:    message.Chat.ID,
					MessageID: lastMessageID,
					Text:      text,
				})
				if err != nil {
					// Ignore "message is not modified" errors
					if !strings.Contains(err.Error(), "message is not modified") {
						log.Printf("Failed to update processing message: %v", err)
					}
				}

				i = (i + 1) % len(dots)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	// Build messages for OpenAI
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

	// Create stream with timeout
	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	stream, err := client.CreateChatCompletionStream(streamCtx, req)
	if err != nil {
		log.Printf("ChatCompletionStream error: %v", err)
		stopAnimation <- true
		_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    message.Chat.ID,
			MessageID: lastMessageID,
			Text:      errorMessage,
		})
		if err != nil {
			log.Printf("Failed to edit message: %v", err)
		}
		return ""
	}
	defer stream.Close()
	user.SetCurrentStream(stream)

	// Stop the animation when we start receiving a response
	stopAnimation <- true
	var messageText string
	responseID := ""
	log.Printf("Stream response started for UserID: %s", user.UserID)

	lastEditTime := time.Now()

	for {
		response, err := stream.Recv()
		if responseID == "" && response.ID != "" {
			responseID = response.ID
		}
		if errors.Is(err, io.EOF) {
			log.Printf("Stream finished, UserID: %s, response ID: %s", user.UserID, responseID)
			user.AddMessage(openai.ChatMessageRoleUser, message.Text)
			user.AddMessage(openai.ChatMessageRoleAssistant, messageText)
			_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:    message.Chat.ID,
				MessageID: lastMessageID,
				Text:      messageText,
				ParseMode: models.ParseModeMarkdown,
			})
			if err != nil {
				log.Printf("Failed to edit message: %v", err)
			}
			user.SetCurrentStream(nil)
			return responseID
		}

		if err != nil {
			log.Printf("Stream error for UserID: %s: %v", user.UserID, err)
			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            err.Error(),
				ParseMode:       models.ParseModeMarkdown,
				MessageThreadID: threadID,
			})
			if err != nil {
				log.Printf("Failed to send error message: %v", err)
			}
			user.SetCurrentStream(nil)
			return responseID
		}

		if len(response.Choices) > 0 {
			messageText += response.Choices[0].Delta.Content

			// Throttle message editing to stay within rate limits (e.g., once every 1 second)
			if time.Since(lastEditTime) > 1*time.Second {
				_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
					ChatID:    message.Chat.ID,
					MessageID: lastMessageID,
					Text:      messageText,
					ParseMode: models.ParseModeMarkdown,
				})
				if err == nil {
					lastEditTime = time.Now()
				}
			}
		} else {
			continue
		}
	}
}

func addVisionMessage(b *bot.Bot, message *models.Message, config *config.Config) openai.ChatCompletionMessage {
	if len(message.Photo) > 0 {
		// Get the largest photo
		photoSize := message.Photo[len(message.Photo)-1]
		fileID := photoSize.FileID

		// Get file info
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

		// Construct file URL: https://api.telegram.org/file/bot<token>/<file_path>
		fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.Token, file.FilePath)
		fmt.Println("Photo URL:", fileURL)

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
