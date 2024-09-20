// Copyright 2017 The Bazel Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package watchman

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"github.com/bazelbuild/bazel-watcher/internal/ibazel/fswatcher/common"
)

func DefaultWatchmanSocketPath() (string, error) {
	watchmanBin, err := exec.LookPath("watchman")
	if err != nil {
		return "", fmt.Errorf("could not find watchman binary: %w", err)
	}
	cmd := exec.Command(watchmanBin, "get-sockname")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not run watchman get-sockname: %w", err)
	}
	return string(output), nil
}

type watchmanWatcher struct {
	root string

	conn   *net.UnixConn
	reader *bufio.Reader
	writer *bufio.Writer

	eventChannel chan common.Event
	replyChannel chan []byte

	subscriptionCounter *atomic.Uint64
}

type watchProjectReply struct {
	Version      string `json:"version"`
	Watch        string `json:"watch"`
	RelativePath string `json:"relative_path"`
}

type subscribeReply struct {
	Version   string `json:"version"`
	Subscribe string `json:"subscribe"`
}

type subscriptionMessage struct {
	Version      string   `json:"version"`
	Clock        string   `json:"clock"`
	Files        []string `json:"files"`
	Root         string   `json:"root"`
	Subscription string   `json:"subscription"`
}

func NewWatchmanWatcher(socketPath string, rootDirectory string) (common.Watcher, error) {
	addr, err := net.ResolveUnixAddr("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("watchman socket address is invalid: %w", err)
	}
	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to watchman: %w", err)
	}
	watcher := watchmanWatcher{
		root:                rootDirectory,
		conn:                conn,
		reader:              bufio.NewReader(conn),
		writer:              bufio.NewWriter(conn),
		eventChannel:        make(chan common.Event),
		replyChannel:        make(chan []byte),
		subscriptionCounter: &atomic.Uint64{},
	}

	// Start a goroutine that will read messages from watchman
	// and dispatch them to our two channels depending on their
	// type
	go func() {
		for {
			// Watchman sent messages are json encoded objects on a single line
			var message []byte
			for {
				bytes, isPrefix, err := watcher.reader.ReadLine()
				if err != nil {
					break
				}
				message = append(message, bytes...)
				if !isPrefix {
					break
				}
			}
			// If we could not read at this point, the connection is broken
			if err != nil {
				return
			}
			// Try to unmarshal the message to a subscription, if it doesn't
			// work, enqueue it on the general channel for replies
			var subscriptionReply subscriptionMessage
			err := json.Unmarshal(message, &subscriptionReply)
			if err != nil {
				watcher.replyChannel <- message
			} else {
				for _, file := range subscriptionReply.Files {
					watcher.eventChannel <- common.Event{
						Name: file,
						Op:   common.Write,
					}
				}
			}
		}
	}()

	_, err = sendReceiveWatchmanMessage[watchProjectReply](watcher, "watch-project", true, rootDirectory)
	if err != nil {
		return nil, fmt.Errorf("failed to call watch-project: %w", err)
	}

	return watcher, nil
}

func (w watchmanWatcher) Close() error {
	return w.conn.Close()
}

func (w watchmanWatcher) UpdateAll(names []string) error {
	ticket := w.subscriptionCounter.Add(1)
	// Cancel a previous subscription if there could have been one
	if ticket > 1 {
		_, err := sendReceiveWatchmanMessage[any](w, "unsubscribe", false, subscriptionName(ticket-1))
		if err != nil {
			return fmt.Errorf("failed to unsubscribe from previous instance: %w", err)
		}
	}
	subQuery := map[string]any{
		"expression": expressionForFilePaths(names),
		"fields":     []string{"name"},
	}
	_, err := sendReceiveWatchmanMessage[subscribeReply](w, "subscribe", true, w.root, subscriptionName(ticket), subQuery)
	return err
}

func (w watchmanWatcher) Events() chan common.Event {
	return w.eventChannel
}

func sendReceiveWatchmanMessage[TResponse any](w watchmanWatcher, command string, needReply bool, params ...any) (TResponse, error) {
	var response TResponse
	var err error

	// Compose and sends a message which is a json encoded
	// array ending with a newline character
	message := []any{
		command,
	}
	message = append(message, params...)

	messageBytes, err := json.Marshal(message)
	messageBytes = append(messageBytes, byte('\n'))
	_, err = w.writer.Write(messageBytes)
	if err != nil {
		return response, fmt.Errorf("could not send watchman message: %w", err)
	}

	if needReply {
		responseBytes := <-w.replyChannel
		err = json.Unmarshal(responseBytes, &response)
	}

	return response, err
}

func subscriptionName(ticket uint64) string {
	return fmt.Sprintf("ibazel-%d", ticket)
}

func expressionForFilePaths(paths []string) []any {
	query := []any{
		"anyof",
	}
	// For each path we want to watch construct a query
	// that specifically matches it
	for _, path := range paths {
		s, err := os.Stat(path)
		if err != nil {
			continue
		}

		if s.IsDir() {
			// If the path is a directory, just match against dirname
			query = append(query, []string{"dirname", path})
		} else {
			// If the path is a file, match the whole thing
			fileExpr := []any{
				"allof",
				[]string{"name", filepath.Base(path)},
				[]string{"dirname", filepath.Dir(path)},
			}
			query = append(query, fileExpr)
		}
	}
	return query
}
