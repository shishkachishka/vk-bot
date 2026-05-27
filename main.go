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
		"7️⃣ Выйти"
}

func getFullMenu() string {
	return getMainMenu() + "\n8️⃣ Привязать Telegram"
}

func handleMessage(vk *api.VK, userID int64, text string) {
	if sessions[userID] == nil {
		sessions[userID] = &UserSession{
			UserID:  userID,
			storage: loadFromAPI(userID),
		}
	}
	session := sessions[userID]

	if session.waitingForTG {
		session.waitingForTG = false
		tgID, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil || tgID <= 0 {
			sendMessage(vk, userID, "❌ Неверный ID. Введите число.")
			return
		}

		tgStorage := loadByTelegramID(tgID)
		if tgStorage != nil {
			sendMessage(vk, userID, "✅ Telegram найден! Введите мастер-пароль от Telegram-аккаунта:")
			session.storage = tgStorage
			session.storage.TelegramID = tgID
			session.IsLoggedIn = false
			return
		}

		session.storage.TelegramID = tgID
		saveToAPI(userID, session.storage)
		sendMessage(vk, userID, fmt.Sprintf("✅ Telegram ID %d привязан!\n⚠️ Удалите сообщение с ID из чата!\n\n%s", tgID, getMainMenu()))
		return
	}

	if !session.IsLoggedIn {
		if text == "Начать" || text == "/start" || text == "start" {
			msg := fmt.Sprintf("🔐 Менеджер паролей\n\n🆔 Ваш VK ID: %d\n\nОтправьте мастер-пароль для входа.\nНет аккаунта? Введите новый пароль (мин. 12 символов).\n\n⚠️ После входа удалите сообщение с паролем!", userID)
			if session.storage.TelegramID > 0 {
				msg += fmt.Sprintf("\n📱 Привязан Telegram ID: %d", session.storage.TelegramID)
			}
			sendMessage(vk, userID, msg)
			return
		}

		if len(session.storage.MasterHash) == 0 {
			if len(text) < 12 {
				sendMessage(vk, userID, "❌ Минимум 12 символов!")
				return
			}
			session.storage.MasterHash = "vk-" + text
			session.storage.MasterSalt = "salt-" + text
			session.IsLoggedIn = true
			saveToAPI(userID, session.storage)
			sendMessage(vk, userID, "✅ Аккаунт создан!\n⚠️ Удалите сообщение с мастер-паролем из чата!\n\n"+getMainMenu())
			return
		}

		if session.storage.MasterHash == "vk-"+text {
			session.IsLoggedIn = true
			if session.storage.TelegramID > 0 {
				tgStorage := loadByTelegramID(session.storage.TelegramID)
				if tgStorage != nil && len(tgStorage.Passwords) > 0 {
					session.storage.Passwords = tgStorage.Passwords
					saveToAPI(userID, session.storage)
					sendMessage(vk, userID, fmt.Sprintf("✅ Вход выполнен!\n📱 Синхронизировано с Telegram!\n⚠️ Удалите сообщение с мастер-паролем из чата!\n\n%s", getMainMenu()))
					return
				}
			}
			sendMessage(vk, userID, "✅ Вход выполнен!\n⚠️ Удалите сообщение с мастер-паролем из чата!\n\n"+getMainMenu())
		} else {
			sendMessage(vk, userID, "❌ Неверный пароль!")
		}
		return
	}

	switch {
	case session.waitingForPass && session.addingNote == "":
		session.addingNote = text
		sendMessage(vk, userID, fmt.Sprintf("📝 Заметка: %s\n🔒 Теперь введите пароль:", text))
		return

	case session.waitingForPass && session.addingNote != "":
		password := text
		entry := PasswordEntry{
			ID:        genID(),
			Note:      session.addingNote,
			Data:      password,
			CreatedAt: time.Now().Format("02.01.2006 15:04"),
		}
		session.storage.Passwords = append(session.storage.Passwords, entry)
		saveID := userID
		if session.storage.TelegramID > 0 {
			saveID = session.storage.TelegramID
		}
		saveToAPI(saveID, session.storage)
		sendMessage(vk, userID, fmt.Sprintf("✅ '%s' сохранен! ID: %s\n⚠️ Удалите сообщение с паролем из чата!\n\n%s", entry.Note, entry.ID, getMainMenu()))
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
				saveID := userID
				if session.storage.TelegramID > 0 {
					saveID = session.storage.TelegramID
				}
				saveToAPI(saveID, session.storage)
				sendMessage(vk, userID, fmt.Sprintf("🗑 '%s' удален!\n\n%s", e.Note, getMainMenu()))
				return
			}
		}
		sendMessage(vk, userID, "❌ Не найдено\n\n"+getMainMenu())
		return
	}

	switch {
	case text == "1" || text == "1️⃣":
		session.waitingForPass = true
		session.addingNote = ""
		sendMessage(vk, userID, "📝 Введите заметку:")
		return

	case text == "2" || text == "2️⃣":
		if len(session.storage.Passwords) == 0 {
			sendMessage(vk, userID, "📭 Пусто\n\n"+getMainMenu())
			return
		}
		resp := "📋 Пароли:\n\n"
		for _, e := range session.storage.Passwords {
			resp += fmt.Sprintf("🔹 %s (ID: %s)\n   📅 %s\n\n", e.Note, e.ID, e.CreatedAt)
		}
		resp += getMainMenu()
		sendMessage(vk, userID, resp)
		return

	case text == "3" || text == "3️⃣":
		session.waitingForGet = true
		sendMessage(vk, userID, "🔍 Введите ID пароля:")
		return

	case text == "4" || text == "4️⃣":
		session.waitingForDel = true
		sendMessage(vk, userID, "🗑 Введите ID пароля для удаления:")
		return

	case text == "5" || text == "5️⃣":
		msg := fmt.Sprintf("🆔 Ваш VK ID: %d", userID)
		if session.storage.TelegramID > 0 {
			msg += fmt.Sprintf("\n📱 Привязан Telegram ID: %d", session.storage.TelegramID)
		}
		sendMessage(vk, userID, msg+"\n\n"+getMainMenu())
		return

	case text == "6" || text == "6️⃣":
		sendMessage(vk, userID, "📘 Инструкция:\n\n1️⃣ Добавить\n2️⃣ Список\n3️⃣ Получить\n4️⃣ Удалить\n5️⃣ ID\n8️⃣ Привязать Telegram\n7️⃣ Выйти\n\n⚠️ Всегда удаляйте сообщения с паролями из чата!\n\n"+getMainMenu())
		return

	case text == "7" || text == "7️⃣":
		delete(sessions, userID)
		sendMessage(vk, userID, "👋 Вы вышли. Напишите 'Начать' для входа.")
		return

	case text == "8" || text == "8️⃣":
		session.waitingForTG = true
		sendMessage(vk, userID, "📱 Введите ваш Telegram ID (узнать в боте @passwordmebot командой /myid):")
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
