package auth

import (
	"fmt"
	"strings"
)

type PermissionKey struct {
	Resource string
	Action   string
}

func (p PermissionKey) String() string {
	return p.Resource + ":" + p.Action
}

var defaultRolePermissions = map[string][]PermissionKey{
	"admin": {
		{Resource: "*", Action: "*"},
	},
	"agent": {
		{Resource: "agents", Action: "read"},
		{Resource: "topics", Action: "read"},
		{Resource: "topics", Action: "write"},
		{Resource: "groups", Action: "read"},
		{Resource: "groups", Action: "write"},
		{Resource: "messages", Action: "read"},
		{Resource: "messages", Action: "write"},
		{Resource: "knowledge", Action: "read"},
		{Resource: "knowledge", Action: "write"},
		{Resource: "collections", Action: "read"},
		{Resource: "collections", Action: "write"},
	},
	"readonly": {
		{Resource: "agents", Action: "read"},
		{Resource: "topics", Action: "read"},
		{Resource: "groups", Action: "read"},
		{Resource: "messages", Action: "read"},
		{Resource: "knowledge", Action: "read"},
		{Resource: "collections", Action: "read"},
	},
	"sync": {
		{Resource: "sync", Action: "read"},
		{Resource: "sync", Action: "write"},
	},
}

func DefaultPermissionsForRole(role string) []PermissionKey {
	return append([]PermissionKey(nil), defaultRolePermissions[strings.ToLower(role)]...)
}

func ValidateRole(role string) error {
	if _, ok := defaultRolePermissions[strings.ToLower(role)]; !ok {
		return fmt.Errorf("unknown role: %s", role)
	}
	return nil
}

func IsAllowed(roles []string, resource, action string, explicit map[string]struct{}) bool {
	for _, role := range roles {
		if strings.EqualFold(role, "admin") {
			return true
		}
	}
	target := resource + ":" + action
	if _, ok := explicit[target]; ok {
		return true
	}
	if _, ok := explicit[resource+":*"]; ok {
		return true
	}
	if _, ok := explicit["*:*"]; ok {
		return true
	}
	return false
}
