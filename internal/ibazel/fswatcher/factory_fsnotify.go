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

//go:build !darwin
// +build !darwin

package fswatcher

import (
	"os"

	"github.com/bazelbuild/bazel-watcher/internal/ibazel/fswatcher/common"
	"github.com/bazelbuild/bazel-watcher/internal/ibazel/fswatcher/fsnotify"
	"github.com/bazelbuild/bazel-watcher/internal/ibazel/fswatcher/watchman"
)

func NewWatcher(workspacePath string) (common.Watcher, error) {
	flag, ok := os.LookupEnv("IBAZEL_USE_WATCHMAN")
	if ok && flag != "0" {
		var socketPath string
		var err error
		if len(flag) > 0 {
			socketPath = flag
		} else {
			socketPath, err = watchman.DefaultWatchmanSocketPath()
			if err != nil {
				return nil, err
			}
		}
		return watchman.NewWatchmanWatcher(socketPath, workspacePath)
	}
	return fsnotify.NewWatcher()
}
