package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SevereCloud/vksdk/v2/api"
	"github.com/SevereCloud/vksdk/v2/api/params"
	"github.com/SevereCloud/vksdk/v2/events"
	"github.com/SevereCloud/vksdk/v2/longpoll-bot"
)

var apiURL = "https://password-bot-1p96.onrender.com"

type PasswordEntry struct {
	ID        string `json:"id"`
	Note      string `json:"note"`
	Data      string `json:"data"`
	CreatedAt string `json:"created_at"`
}

type Storage struct {
	MasterHash string          `json:"master_hash"`
	MasterSalt string          `json:"master_salt"`
	TelegramID int64           `json:"telegram_id"`
	Passwords  []PasswordEntry `json:"passwords"`
}

type UserSession struct {
	UserID         int64
	IsLoggedIn     bool
	storage        *Storage
	addingNote     string
	waitingForPass bool
	waitingForGet  bool
	waitingForDel  bool
	waitingForTG   bool
}

var sessions = make(map[int64]*UserSession)

func genID() string {
	const letters = "abcdef0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func loadFromAPI(userID int64) *Storage {
	url := fmt.Sprintf("%s/api/vk/load?id=%d", apiURL, userID)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		return &Storage{Passwords: []PasswordEntry{}}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var storage Storage
	json.Unmarshal(data, &storage)
	return &storage
}

func saveToAPI(userID int64, storage *Storage) {
	url := fmt.Sprintf("%s/api/vk/save?id=%d", apiURL, userID)
	data, _ := json.Marshal(storage)
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	client.Do(req)
}

func loadByTelegramID(tgID int64) *Storage {
	url := fmt.Sprintf("%s/api/vk/load?id=%d", apiURL, tgID)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var storage Storage
	json.Unmarshal(data, &storage)
	if len(storage.MasterHash) > 0 {
		return &storage
	}
	return nil
}

func sendMessage(vk *api.VK, userID int64, text string) {
	msg := params.NewMessagesSendBuilder()
	msg.PeerID(int(userID))
	msg.RandomID(rand.Intn(9999999))
	msg.Message(text)
	vk.MessagesSend(msg.Params)
}

func getMainMenu() string {
	return "📋 Меню:\n" +
		"1️⃣ Добавить пароль\n" +
		"2️⃣ Список\n" +
		"3️⃣ Получить пароль\n" +
		"4️⃣ Удалить\n" +
		"5️⃣ Мой ID\n" +
		"6️⃣ Инструкция\n" +
		"7️⃣ Выйти\n" +
		"8️⃣ Привязать Telegram"
}

func handleMessage(vk *api.VK, userID int64, text string) {
	// Игнорируем пустые сообщения
	if text == "" {
		return
	}

	if sessions[userID] == nil {
		sessions[userID] = &UserSession{
			UserID:  userID,
			storage: loadFromAPI(userID),
		}
	}
	session := sessions[userID]

	// Ожидание ввода Telegram ID
	if session.waitingForTG {
		session.waitingForTG = false
		tgID, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil || tgID <= 0 {
			sendMessage(vk, userID, "❌ Неверный ID. Введите число.")
			session.waitingForTG = true
			return
		}

		session.storage.TelegramID = tgID
		saveToAPI(userID, session.storage)
		sendMessage(vk, userID, fmt.Sprintf("✅ Telegram ID %d привязан!\n⚠️ Удалите сообщение с ID из чата!\n\n%s", tgID, getMainMenu()))
		return
	}

	// НЕ авторизован
	if !session.IsLoggedIn {
		// Команда "Начать"
		if text == "Начать" || text == "/start" || text == "start" {
			msg := fmt.Sprintf("🔐 Менеджер паролей\n\n🆔 Ваш VK ID: %d\n\nВведите мастер-пароль (мин. 12 символов).\nНет аккаунта? Просто придумайте новый пароль.", userID)
			sendMessage(vk, userID, msg)
			return
		}

		// Новый аккаунт (нет MasterHash)
		if len(session.storage.MasterHash) == 0 {
			if len(text) < 12 {
				sendMessage(vk, userID, "❌ Минимум 12 символов!")
				return
			}
			session.storage.MasterHash = "vk-" + text
			session.storage.MasterSalt = "salt-" + text
			session.IsLoggedIn = true
			saveToAPI(userID, session.storage)
			sendMessage(vk, userID, "✅ Аккаунт создан!\n⚠️ Удалите сообщение с паролем!\n\n"+getMainMenu())
			return
		}

		// Вход существующего аккаунта
		if session.storage.MasterHash == "vk-"+text {
			session.IsLoggedIn = true
			sendMessage(vk, userID, "✅ Вход выполнен!\n⚠️ Удалите сообщение с паролем!\n\n"+getMainMenu())
			return
		}

		sendMessage(vk, userID, "❌ Неверный пароль!")
		return
	}

	// АВТОРИЗОВАН — обрабатываем состояния
	switch {
	case session.waitingForPass:
		if session.addingNote == "" {
			session.addingNote = text
			sendMessage(vk, userID, "🔒 Введите пароль:")
			return
		}
		// Сохраняем пароль
		password := text
		entry := PasswordEntry{
			ID:        genID(),
			Note:      session.addingNote,
			Data:      password,
			CreatedAt: time.Now().Format("02.01.2006 15:04"),
		}
		session.storage.Passwords = append(session.storage.Passwords, entry)
		saveToAPI(userID, session.storage)
		sendMessage(vk, userID, fmt.Sprintf("✅ '%s' сохранен! ID: %s\n\n%s", entry.Note, entry.ID, getMainMenu()))
		session.addingNote = ""
		session.waitingForPass = false
		return

	case session.waitingForGet:
		id := strings.TrimSpace(text)
		session.waitingForGet = false
		for _, e := range session.storage.Passwords {
			if e.ID == id {
				sendMessage(vk, userID, fmt.Sprintf("🔑 %s: %s\n\n%s", e.Note, e.Data, getMainMenu()))
				return
			}
		}
		sendMessage(vk, userID, "❌ Не найдено\n\n"+getMainMenu())
		return

	case session.waitingForDel:
		id := strings.TrimSpace(text)
		session.waitingForDel = false
		for i, e := range session.storage.Passwords {
			if e.ID == id {
				session.storage.Passwords = append(session.storage.Passwords[:i], session.storage.Passwords[i+1:]...)
				saveToAPI(userID, session.storage)
				sendMessage(vk, userID, fmt.Sprintf("🗑 '%s' удален!\n\n%s", e.Note, getMainMenu()))
				return
			}
		}
		sendMessage(vk, userID, "❌ Не найдено\n\n"+getMainMenu())
		return
	}

	// Обработка команд меню
	switch text {
	case "1", "1️⃣":
		session.waitingForPass = true
		session.addingNote = ""
		sendMessage(vk, userID, "📝 Введите заметку:")
		return

	case "2", "2️⃣":
		if len(session.storage.Passwords) == 0 {
			sendMessage(vk, userID, "📭 Пусто\n\n"+getMainMenu())
			return
		}
		resp := "📋 Пароли:\n\n"
		for _, e := range session.storage.Passwords {
			resp += fmt.Sprintf("🔹 %s (ID: %s)\n   📅 %s\n\n", e.Note, e.ID, e.CreatedAt)
		}
		sendMessage(vk, userID, resp+getMainMenu())
		return

	case "3", "3️⃣":
		session.waitingForGet = true
		sendMessage(vk, userID, "🔍 Введите ID пароля:")
		return

	case "4", "4️⃣":
		session.waitingForDel = true
		sendMessage(vk, userID, "🗑 Введите ID пароля для удаления:")
		return

	case "5", "5️⃣":
		msg := fmt.Sprintf("🆔 Ваш VK ID: %d", userID)
		if session.storage.TelegramID > 0 {
			msg += fmt.Sprintf("\n📱 Привязан Telegram ID: %d", session.storage.TelegramID)
		}
		sendMessage(vk, userID, msg+"\n\n"+getMainMenu())
		return

	case "6", "6️⃣":
		sendMessage(vk, userID, "📘 Команды:\n1-Добавить 2-Список 3-Получить 4-Удалить\n5-ID 7-Выйти 8-Привязать Telegram\n\n⚠️ Удаляйте сообщения с паролями!\n\n"+getMainMenu())
		return

	case "7", "7️⃣":
		delete(sessions, userID)
		sendMessage(vk, userID, "👋 Вы вышли.")
		return

	case "8", "8️⃣":
		session.waitingForTG = true
		sendMessage(vk, userID, "📱 Введите Telegram ID (узнать в @passwordmebot):")
		return

	default:
		sendMessage(vk, userID, getMainMenu())
	}
}

func main() {
	token := os.Getenv("VK_TOKEN")
	if token == "" {
		log.Fatal("VK_TOKEN не задан")
	}

	vk := api.NewVK(token)

	groupID := 0
	fmt.Sscanf(os.Getenv("VK_GROUP_ID"), "%d", &groupID)
	if groupID == 0 {
		log.Fatal("VK_GROUP_ID не задан")
	}

	lp, err := longpoll.NewLongPoll(vk, groupID)
	if err != nil {
		log.Fatal(err)
	}

	lp.MessageNew(func(ctx context.Context, obj events.MessageNewObject) {
		userID := int64(obj.Message.PeerID)
		text := obj.Message.Text
		handleMessage(vk, userID, text)
	})

	log.Println("ВК бот запущен")
	if err := lp.Run(); err != nil {
		log.Fatal(err)
	}
}
