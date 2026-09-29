package main

import json "github.com/goccy/go-json"

type gmActionFunc func(*Server, int64, json.RawMessage) (string, map[string]any, string, string)

var gmActionRegistry = map[string]gmActionFunc{}

func registerGMAction(action string, fn gmActionFunc) {
	if action == "" || fn == nil {
		return
	}
	gmActionRegistry[action] = fn
}

func dispatchRegisteredGMAction(s *Server, action string, playerID int64, payload json.RawMessage) (bool, string, map[string]any, string, string) {
	fn, ok := gmActionRegistry[action]
	if !ok {
		return false, "", nil, "", ""
	}
	status, data, code, msg := fn(s, playerID, payload)
	return true, status, data, code, msg
}
