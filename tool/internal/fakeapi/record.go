package fakeapi

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Event struct {
	Time        time.Time         `json:"time"`
	Bot         string            `json:"bot"`
	BotName     string            `json:"bot_name,omitempty"`
	Method      string            `json:"method"`
	Mode        string            `json:"mode"`
	ChatID      string            `json:"chat_id,omitempty"`
	ThreadID    string            `json:"thread_id,omitempty"`
	MessageID   string            `json:"message_id,omitempty"`
	AssignedID  int64             `json:"assigned_message_id,omitempty"`
	Text        string            `json:"text,omitempty"`
	ReplyMarkup string            `json:"reply_markup,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
}

type Recorder struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	f       *os.File
	size    int64
}

func NewRecorder(path string, maxSize int64) (*Recorder, error) {
	if maxSize <= 0 {
		maxSize = 100 << 20
	}
	r := &Recorder{path: path, maxSize: maxSize}
	return r, r.open()
}

func (r *Recorder) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Recorder) Write(ev Event) {
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return
	}
	if r.size+int64(len(line)) > r.maxSize {
		r.f.Close()
		_ = os.Rename(r.path, r.path+".1")
		if r.open() != nil {
			r.f = nil
			return
		}
	}
	n, _ := r.f.Write(line)
	r.size += int64(n)
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}
