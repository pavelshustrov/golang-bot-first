package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

const (
	// Command prefixs
	AssignCmd   = "/assign_"
	NewCmd      = "/new "
	TasksCmd    = "/tasks"
	UnassignCmd = "/unassign_"
	ResolveCmd  = "/resolve_"
	MyCmd       = "/my"
	OwnerCmd    = "/owner"
)

var (
	WebhookURL string
	BotToken   string
)

func init() {
	BotToken = os.Getenv("TOKEN")
	WebhookURL = os.Getenv("WEBHOOK_PREFIX")
}

type Task struct {
	id       uint64
	owned    *string
	assignee *string
	text     string
}

type TaskBot struct {
	bot    *tgbotapi.BotAPI
	taskID uint64
	taskMu sync.RWMutex

	userChats map[string]int64
	assigned  map[string]map[uint64]*Task
	owned     map[string]map[uint64]*Task
	allTasks  map[uint64]*Task
}

func NewTaskBot(bot *tgbotapi.BotAPI) *TaskBot {
	return &TaskBot{
		bot:       bot,
		taskID:    0,
		taskMu:    sync.RWMutex{},
		userChats: make(map[string]int64),
		assigned:  make(map[string]map[uint64]*Task),
		owned:     make(map[string]map[uint64]*Task),
		allTasks:  make(map[uint64]*Task),
	}
}

func (taskBot *TaskBot) send(username string, text string) error {
	chatID := taskBot.userChats[username]
	messageConfig := tgbotapi.NewMessage(chatID, text)
	_, err := taskBot.bot.Send(messageConfig)
	return err
}

func (taskBot *TaskBot) sortedTasks(ids []uint64) []*Task {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tasks := make([]*Task, 0, len(ids))

	for _, id := range ids {
		tasks = append(tasks, taskBot.allTasks[id])
	}

	return tasks
}

func (taskBot *TaskBot) new(chatID int64, username string, text string) {
	id := atomic.AddUint64(&taskBot.taskID, 1)
	task := &Task{id: id, text: text, owned: &username, assignee: nil}

	taskBot.taskMu.Lock()
	defer taskBot.taskMu.Unlock()

	taskBot.userChats[username] = chatID
	taskBot.allTasks[id] = task
	taskBot.owned[username][id] = task

	taskBot.send(username, fmt.Sprintf(`Задача "%s" создана, id=%d`, text, id))
}

func (taskBot *TaskBot) owner(username string) {
	taskBot.taskMu.RLock()
	defer taskBot.taskMu.RUnlock()

	usernameTasks := taskBot.owned[username]

	ids := make([]uint64, 0, len(usernameTasks))
	for id := range usernameTasks {
		ids = append(ids, id)
	}
	tasks := taskBot.sortedTasks(ids)
	messages := make([]string, 0, len(ids))
	for _, task := range tasks {
		var assign string
		if task.assignee == nil {
			assign = fmt.Sprintf("\n/assign_%d", task.id)
		}

		messages = append(messages, fmt.Sprintf("%d. %s by %s%s", task.id, task.text, *task.owned, assign))
	}

	taskBot.send(username, strings.Join(messages, "\n\n"))
}

func (taskBot *TaskBot) my(username string) {
	taskBot.taskMu.RLock()
	defer taskBot.taskMu.RUnlock()

	usernameTasks := taskBot.assigned[username]
	if len(usernameTasks) == 0 {
		taskBot.send(username, "Нет задач")
		return
	}

	for _, task := range usernameTasks {
		taskBot.send(username, fmt.Sprintf("%d. %s by %s\n/unassign_%d /resolve_%d", task.id, task.text, *task.owned, task.id, task.id))
	}
}

func (taskBot *TaskBot) tasks(me string) {
	taskBot.taskMu.RLock()
	defer taskBot.taskMu.RUnlock()

	if len(taskBot.allTasks) == 0 {
		taskBot.send(me, "Нет задач")
		return
	}

	ids := make([]uint64, 0, len(taskBot.allTasks))
	for id := range taskBot.allTasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	messages := make([]string, 0, len(taskBot.allTasks))
	for _, taskID := range ids {
		task := taskBot.allTasks[taskID]
		var assigne string

		if task.assignee == nil {
			assigne = fmt.Sprintf("\n/assign_%d", task.id)
		} else {
			if *task.assignee == me {
				assigne = fmt.Sprintf("\nassignee: я\n/unassign_%d /resolve_%d", task.id, task.id)
			} else {
				assigne = fmt.Sprintf("\nassignee: %s", *task.assignee)
			}
		}
		messages = append(messages, fmt.Sprintf("%d. %s by %s%s", task.id, task.text, *task.owned, assigne))
	}
	taskBot.send(me, strings.Join(messages, "\n\n"))
}

func (taskBot *TaskBot) resolve(username string, taskID uint64) {
	taskBot.taskMu.Lock()
	defer taskBot.taskMu.Unlock()

	task := taskBot.allTasks[taskID]
	if username != *task.assignee {
		taskBot.send(username, "Задача не на вас")
		return
	}

	delete(taskBot.assigned[username], taskID)
	delete(taskBot.owned[*task.owned], taskID)
	delete(taskBot.allTasks, taskID)

	taskBot.send(username, fmt.Sprintf(`Задача "%s" выполнена`, task.text))
	if *task.owned != *task.assignee {
		taskBot.send(*task.owned, fmt.Sprintf(`Задача "%s" выполнена %s`, task.text, username))
	}
}

func (taskBot *TaskBot) assign(username string, taskID uint64) {
	taskBot.taskMu.Lock()
	defer taskBot.taskMu.Unlock()

	task := taskBot.allTasks[taskID]

	var prev string
	if task.assignee != nil {
		prev = *task.assignee
		delete(taskBot.assigned[*task.assignee], taskID)
	} else {
		prev = *task.owned
	}

	task.assignee = &username
	taskBot.assigned[username][taskID] = task

	taskBot.send(username, fmt.Sprintf(`Задача "%s" назначена на вас`, task.text))
	if prev != username {
		taskBot.send(prev, fmt.Sprintf(`Задача "%s" назначена на %s`, task.text, username))
	}
}

func (taskBot *TaskBot) unassign(username string, taskID uint64) {
	taskBot.taskMu.Lock()
	defer taskBot.taskMu.Unlock()

	task := taskBot.allTasks[taskID]
	if task.assignee == nil || username != *task.assignee {
		taskBot.send(username, "Задача не на вас")
		return
	}

	delete(taskBot.assigned[username], taskID)
	assigned := *task.assignee
	task.assignee = nil

	taskBot.send(username, `Принято`)
	if *task.owned != assigned {
		taskBot.send(*task.owned, fmt.Sprintf(`Задача "%s" осталась без исполнителя`, task.text))
	}
}

func (taskBot *TaskBot) registerUser(chatID int64, username string) {
	taskBot.userChats[username] = chatID
	if assigned := taskBot.assigned[username]; assigned == nil {
		taskBot.assigned[username] = make(map[uint64]*Task)
	}
	if owned := taskBot.owned[username]; owned == nil {
		taskBot.owned[username] = make(map[uint64]*Task)
	}
}

func (taskBot *TaskBot) handle(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	text := update.Message.Text
	username := "@" + update.Message.From.UserName
	chatID := update.Message.Chat.ID

	taskBot.registerUser(chatID, username)

	if strings.HasPrefix(text, AssignCmd) {
		taskID, err := strconv.Atoi(text[len(AssignCmd):])
		if err != nil {
			taskBot.bot.Send(tgbotapi.NewMessage(chatID, "taskID should be numeric"))
			return
		}
		taskBot.assign(username, uint64(taskID))
	} else if strings.HasPrefix(text, UnassignCmd) {
		taskID, err := strconv.Atoi(text[len(UnassignCmd):])
		if err != nil {
			taskBot.bot.Send(tgbotapi.NewMessage(chatID, "taskID should be numeric"))
			return
		}
		taskBot.unassign(username, uint64(taskID))
	} else if strings.HasPrefix(text, ResolveCmd) {
		taskID, err := strconv.Atoi(text[len(ResolveCmd):])
		if err != nil {
			taskBot.bot.Send(tgbotapi.NewMessage(chatID, "taskID should be numeric"))
			return
		}
		taskBot.resolve(username, uint64(taskID))
	} else if strings.HasPrefix(text, MyCmd) {
		taskBot.my(username)
	} else if strings.HasPrefix(text, TasksCmd) {
		taskBot.tasks(username)
	} else if strings.HasPrefix(text, NewCmd) {
		taskBot.new(chatID, username, text[len(NewCmd):])
	} else if strings.HasPrefix(text, OwnerCmd) {
		taskBot.owner(username)
	}
}

func startTaskBot(ctx context.Context) error {
	bot, err := tgbotapi.NewBotAPI(BotToken)
	if err != nil {
		fmt.Printf("failed to create BotAPI instance %v", err)
		return err
	}

	_, err = bot.SetWebhook(tgbotapi.NewWebhook(WebhookURL))
	if err != nil {
		fmt.Printf("failed to create BotAPI instance %v", err)
		return err
	}

	taskBot := NewTaskBot(bot)
	updates := bot.ListenForWebhook("/")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	server := &http.Server{Addr: fmt.Sprintf(":%s", port)}

	go func() {
		server.ListenAndServe()
	}()

	go func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				server.Shutdown(context.Background())
				return
			case update := <-updates:
				taskBot.handle(update)
			}
		}
	}(ctx)

	return nil
}
