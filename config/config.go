package config

import (
	"fmt"
	"log"
	"reflect"

	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"

	"openrouter-bot/lang"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	TelegramBotToken   string
	OpenAIApiKey       string
	Model              ModelParameters
	MaxTokens          int
	BotLanguage        string
	OpenAIBaseURL      string
	SystemPrompt       string
	BudgetPeriod       string
	GuestBudget        float64
	UserBudget         float64
	AdminChatIDs       []int64
	AllowedUserChatIDs []int64
	MaxHistorySize     int
	MaxHistoryTime     int
	Vision             string
	VisionPrompt       string
	VisionDetails      string
	StatsMinRole       string
	Lang               string
}

type ModelParameters struct {
	Type              string
	ModelName         string
	ModelNameDefault  string
	ModelReq          openai.ChatCompletionRequest
	FrequencyPenalty  float64
	MinP              float64
	PresencePenalty   float64
	RepetitionPenalty float64
	Temperature       float64
	TopA              float64
	TopK              float64
	TopP              float64
}

func Load() (*Config, error) {
	_ = godotenv.Load() // Optional: ignore error if .env is missing

	// Default params
	viper.SetDefault("MAX_TOKENS", 2000)
	viper.SetDefault("TEMPERATURE", 0.7)
	viper.SetDefault("TOP_P", 0.7)
	viper.SetDefault("BASE_URL", "https://openrouter.ai/api/v1") // or https://api.openai.com/v1
	viper.SetDefault("BUDGET_PERIOD", "monthly")
	viper.SetDefault("MAX_HISTORY_SIZE", 10)
	viper.SetDefault("MAX_HISTORY_TIME", 60)
	viper.SetDefault("LANG", "en")

	config := &Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		OpenAIApiKey:     os.Getenv("API_KEY"),
		Model: ModelParameters{
			Type:             viper.GetString("TYPE"),
			ModelName:        viper.GetString("MODEL"),
			ModelNameDefault: viper.GetString("MODEL"),
			Temperature:      viper.GetFloat64("TEMPERATURE"),
			TopP:             viper.GetFloat64("TOP_P"),
		},
		MaxTokens:          viper.GetInt("MAX_TOKENS"),
		OpenAIBaseURL:      viper.GetString("BASE_URL"),
		SystemPrompt:       viper.GetString("ASSISTANT_PROMPT"),
		BudgetPeriod:       viper.GetString("BUDGET_PERIOD"),
		GuestBudget:        viper.GetFloat64("GUEST_BUDGET"),
		UserBudget:         viper.GetFloat64("USER_BUDGET"),
		AdminChatIDs:       getStrAsIntList("ADMIN_IDS"),
		AllowedUserChatIDs: getStrAsIntList("ALLOWED_USER_IDS"),
		MaxHistorySize:     viper.GetInt("MAX_HISTORY_SIZE"),
		MaxHistoryTime:     viper.GetInt("MAX_HISTORY_TIME"),
		Vision:             viper.GetString("VISION"),
		VisionPrompt:       viper.GetString("VISION_PROMPT"),
		VisionDetails:      viper.GetString("VISION_DETAIL"),
		StatsMinRole:       viper.GetString("STATS_MIN_ROLE"),
		Lang:               viper.GetString("LANG"),
	}
	if config.BudgetPeriod == "" {
		log.Fatalf("Set budget_period in config file")
	}
	language := lang.Translate("language", config.Lang)
	config.SystemPrompt = "Always answer in " + language + " language. You must format your response using Telegram-compatible HTML tags (<b>, <i>, <code>, <pre>, <a href>). Do not use Markdown." + config.SystemPrompt
	printConfig(config)
	return config, nil
}

func getStrAsIntList(name string) []int64 {
	valueStr := viper.GetString(name)
	if valueStr == "" {
		log.Println("Missing required environment variable, " + name)
		var emptyArray []int64
		return emptyArray
	}
	var values []int64
	for _, str := range strings.Split(valueStr, ",") {
		value, err := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		if err != nil {
			log.Printf("Invalid value for environment variable %s: %v", name, err)
			continue
		}
		values = append(values, value)
	}
	return values
}

func printConfig(c *Config) {
	if c == nil {
		fmt.Println("Config is nil")
		return
	}
	v := reflect.ValueOf(*c)
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldName := t.Field(i).Name

		value := field.Interface()
		// Redact sensitive fields
		if fieldName == "TelegramBotToken" || fieldName == "OpenAIApiKey" {
			value = "[REDACTED]"
		}

		if field.Kind() == reflect.Struct {
			fmt.Printf("%s:\n", fieldName)
			printStructFields(field)
		} else {
			fmt.Printf("%s: %v\n", fieldName, value)
		}
	}
}

func printStructFields(v reflect.Value) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldName := t.Field(i).Name
		fmt.Printf("  %s: %v\n", fieldName, field.Interface())
	}
}
