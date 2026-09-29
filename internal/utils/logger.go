package utils

import (
	"fmt"
	"log"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorGray   = "\033[90m"
)

func LogInfo(component, message string, args ...interface{}) {
	formattedMessage := message
	if len(args) > 0 {
		formattedMessage = fmt.Sprintf(message, args...)
	}
	log.Printf("%s[INFO]%s %s[%s]%s %s",
		ColorBlue, ColorReset,
		ColorCyan, component, ColorReset,
		formattedMessage)
}

func LogSuccess(component, message string, args ...interface{}) {
	formattedMessage := message
	if len(args) > 0 {
		formattedMessage = fmt.Sprintf(message, args...)
	}
	log.Printf("%s[SUCCESS]%s %s[%s]%s %s",
		ColorGreen, ColorReset,
		ColorCyan, component, ColorReset,
		formattedMessage)
}

func LogWarning(component, message string, args ...interface{}) {
	formattedMessage := message
	if len(args) > 0 {
		formattedMessage = fmt.Sprintf(message, args...)
	}
	log.Printf("%s[WARNING]%s %s[%s]%s %s",
		ColorYellow, ColorReset,
		ColorCyan, component, ColorReset,
		formattedMessage)
}

func LogError(component, message string, err error) {
	if err != nil {
		log.Printf("%s[ERROR]%s %s[%s]%s %s: %s%v%s",
			ColorRed, ColorReset,
			ColorCyan, component, ColorReset,
			message,
			ColorRed, err, ColorReset)
	} else {
		log.Printf("%s[ERROR]%s %s[%s]%s %s",
			ColorRed, ColorReset,
			ColorCyan, component, ColorReset,
			message)
	}
}

func LogDebug(component, message string, args ...interface{}) {
	formattedMessage := message
	if len(args) > 0 {
		formattedMessage = fmt.Sprintf(message, args...)
	}
	log.Printf("%s[DEBUG]%s %s[%s]%s %s",
		ColorPurple, ColorReset,
		ColorCyan, component, ColorReset,
		formattedMessage)
}

func LogRequest(method, path, userID string) {
	log.Printf("%s[REQUEST]%s %s%s%s %s | UserID: %s%s%s",
		ColorCyan, ColorReset,
		ColorWhite, method, ColorReset,
		path,
		ColorYellow, userID, ColorReset)
}

func LogResponse(path string, statusCode int, duration time.Duration) {
	color := ColorGreen
	if statusCode >= 400 && statusCode < 500 {
		color = ColorYellow
	} else if statusCode >= 500 {
		color = ColorRed
	}

	log.Printf("%s[RESPONSE]%s %s | Status: %s%d%s | Duration: %s%v%s",
		ColorGray, ColorReset,
		path,
		color, statusCode, ColorReset,
		ColorWhite, duration, ColorReset)
}

func LogDB(operation, query string) {
	log.Printf("%s[DB]%s %s[%s]%s %s",
		ColorGray, ColorReset,
		ColorWhite, operation, ColorReset,
		query)
}
