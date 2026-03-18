package main

import (
	"context"
	"fmt"
	"log"
	"openrouter-bot/api"
	"openrouter-bot/config"
	"openrouter-bot/lang"
	"openrouter-bot/user"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/sashabaranov/go-openai"
)

type contextKey string

const (
	userManagerKey   contextKey = "userManager"
	clientKey        contextKey = "client"
	configManagerKey contextKey = "configManager"
)

const (
	ParseModeHTML       = "HTML"
	ParseModeMarkdownV2 = "MarkdownV2"
	ParseModeMarkdown   = "Markdown"
)

func main() {
	err := lang.LoadTranslations("./lang/")
	if err != nil {
		log.Fatalf("Error loading translations: %v", err)
	}

	manager, err := config.NewManager("./config.yaml")
	if err != nil {
		log.Fatalf("Error initializing config manager: %v", err)
	}

	conf := manager.GetConfig()

	// Create context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Initialize bot
	b, err := bot.New(conf.TelegramBotToken, bot.WithDefaultHandler(createUpdateHandler(manager)))
	if err != nil {
		log.Panic(err)
	}

	// Set bot commands
	commands := []models.BotCommand{
		{Command: "start", Description: lang.Translate("description.start", conf.Lang)},
		{Command: "help", Description: lang.Translate("description.help", conf.Lang)},
		{Command: "get_models", Description: lang.Translate("description.getModels", conf.Lang)},
		{Command: "set_model", Description: lang.Translate("description.setModel", conf.Lang)},
		{Command: "reset", Description: lang.Translate("description.reset", conf.Lang)},
		{Command: "stats", Description: lang.Translate("description.stats", conf.Lang)},
		{Command: "stop", Description: lang.Translate("description.stop", conf.Lang)},
	}
	_, err = b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: commands,
	})
	if err != nil {
		log.Fatalf("Failed to set bot commands: %v", err)
	}

	clientOptions := openai.DefaultConfig(conf.OpenAIApiKey)
	clientOptions.BaseURL = conf.OpenAIBaseURL
	client := openai.NewClientWithConfig(clientOptions)

	userManager := user.NewUserManager("logs")

	// Deduplicator to prevent double-processing of updates
	processedUpdates := &sync.Map{}
	// Cleanup old updates occasionally
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			processedUpdates.Range(func(key, value any) bool {
				if t, ok := value.(time.Time); ok && time.Since(t) > 10*time.Minute {
					processedUpdates.Delete(key)
				}
				return true
			})
		}
	}()

	// Store user manager in context for handlers to access
	ctx = context.WithValue(ctx, userManagerKey, userManager)
	ctx = context.WithValue(ctx, clientKey, client)
	ctx = context.WithValue(ctx, configManagerKey, manager)
	ctx = context.WithValue(ctx, "processedUpdates", processedUpdates)

	log.Println("Starting bot...")
	b.Start(ctx)
}

// createUpdateHandler returns the main update handler
func createUpdateHandler(manager *config.Manager) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}

		// Deduplicate updates
		processedUpdates := ctx.Value("processedUpdates").(*sync.Map)
		if _, loaded := processedUpdates.LoadOrStore(update.ID, time.Now()); loaded {
			log.Printf("Ignoring duplicate update %d", update.ID)
			return
		}

		// Extract message thread ID for forum topic support
		threadID := update.Message.MessageThreadID

		userManager := ctx.Value(userManagerKey).(*user.Manager)
		conf := manager.GetConfig()
		userStats := userManager.GetUser(update.Message.From.ID, update.Message.From.Username, conf)

		// Check if message is a command
		if update.Message.Text != "" && strings.HasPrefix(update.Message.Text, "/") {
			handleCommand(ctx, b, update, userStats, threadID, manager)
		} else {
			// Handle non-command messages in goroutine
			go handleNonCommandMessage(ctx, b, update, userStats, threadID, manager)
		}
	}
}

// parseCommand extracts command and arguments from message text
func parseCommand(text string) (command string, args string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}

	// Remove command prefix
	text = text[1:]

	// Split command and arguments
	parts := strings.SplitN(text, " ", 2)
	command = parts[0]

	// Handle command@botname format
	if idx := strings.Index(command, "@"); idx != -1 {
		command = command[:idx]
	}

	if len(parts) > 1 {
		args = parts[1]
	}

	return command, args
}

// handleCommand processes bot commands
func handleCommand(ctx context.Context, b *bot.Bot, update *models.Update, userStats *user.UsageTracker, threadID int, manager *config.Manager) {
	msg := update.Message
	conf := manager.GetConfig()

	command, args := parseCommand(msg.Text)
	argsArr := strings.Split(args, " ")

	switch command {
	case "start":
		msgText := lang.Translate("commands.start", conf.Lang) + lang.Translate("commands.help", conf.Lang) + lang.Translate("commands.start_end", conf.Lang)
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            msgText,
			ParseMode:       ParseModeHTML,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "help":
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            lang.Translate("commands.help", conf.Lang),
			ParseMode:       ParseModeHTML,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "get_models":
		models, _ := api.GetFreeModels(conf)
		text := lang.Translate("commands.getModels", conf.Lang) + models
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            text,
			ParseMode:       ParseModeMarkdownV2,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "set_model":
		responseText := conf.Model.ModelName

		switch {
		case args == "default":
			conf.Model.ModelName = conf.Model.ModelNameDefault
			responseText = lang.Translate("commands.setModel", conf.Lang) + " `" + conf.Model.ModelName + "`"
		case args == "":
			responseText = lang.Translate("commands.noArgsModel", conf.Lang)
		case len(argsArr) > 1:
			responseText = lang.Translate("commands.noSpaceModel", conf.Lang)
		default:
			conf.Model.ModelName = argsArr[0]
			responseText = lang.Translate("commands.setModel", conf.Lang) + " `" + conf.Model.ModelName + "`"
		}

		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            responseText,
			ParseMode:       ParseModeMarkdownV2,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "reset":
		responseText := ""

		if args == "system" {
			userStats.SetSystemPrompt(conf.SystemPrompt)
			responseText = lang.Translate("commands.reset_system", conf.Lang)
		} else if args != "" {
			userStats.SetSystemPrompt(args)
			responseText = lang.Translate("commands.reset_prompt", conf.Lang) + args + "."
		} else {
			userStats.ClearHistory()
			responseText = lang.Translate("commands.reset", conf.Lang)
		}

		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            responseText,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "stats":
		userStats.CheckHistory(conf.MaxHistorySize, conf.MaxHistoryTime)
		countedUsage := strconv.FormatFloat(userStats.GetCurrentCost(conf.BudgetPeriod), 'f', 6, 64)
		todayUsage := strconv.FormatFloat(userStats.GetCurrentCost("daily"), 'f', 6, 64)
		monthUsage := strconv.FormatFloat(userStats.GetCurrentCost("monthly"), 'f', 6, 64)
		totalUsage := strconv.FormatFloat(userStats.GetCurrentCost("total"), 'f', 6, 64)
		messagesCount := strconv.Itoa(len(userStats.GetMessages()))

		var statsMessage string
		if userStats.CanViewStats(conf) {
			statsMessage = fmt.Sprintf(
				lang.Translate("commands.stats", conf.Lang),
				countedUsage, todayUsage, monthUsage, totalUsage, messagesCount)
		} else {
			statsMessage = fmt.Sprintf(
				lang.Translate("commands.stats_min", conf.Lang), messagesCount)
		}

		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: threadID,
			Text:            statsMessage,
			ParseMode:       ParseModeHTML,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}

	case "stop":
		stream := userStats.GetCurrentStream()
		if stream != nil {
			stream.Close()
			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          msg.Chat.ID,
				MessageThreadID: threadID,
				Text:            lang.Translate("commands.stop", conf.Lang),
			})
			if err != nil {
				log.Printf("Failed to send message: %v", err)
			}
		} else {
			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          msg.Chat.ID,
				MessageThreadID: threadID,
				Text:            lang.Translate("commands.stop_err", conf.Lang),
			})
			if err != nil {
				log.Printf("Failed to send message: %v", err)
			}
		}
	}
}

// handleNonCommandMessage processes non-command messages
func handleNonCommandMessage(ctx context.Context, b *bot.Bot, update *models.Update, userStats *user.UsageTracker, threadID int, manager *config.Manager) {
	client := ctx.Value(clientKey).(*openai.Client)
	conf := manager.GetConfig()

	if userStats.HaveAccess(conf) {
		responseID := api.HandleChatGPTStreamResponse(b, client, update.Message, conf, userStats, threadID)
		if conf.Model.Type == "openrouter" && responseID != "" {
			userStats.GetUsageFromApi(responseID, conf)
		}
	} else {
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: threadID,
			Text:            lang.Translate("budget_out", conf.Lang),
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}
	}
}
