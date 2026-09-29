package main

type playerCommandRoute struct {
	action     string
	permission string
}

var extraPlayerCommandRoutes = map[string]playerCommandRoute{}
var extraPlayerSubresources = map[string]string{}
var extraRolePermissions = map[string]map[string]bool{}

func registerPlayerCommandRoute(path, action, permission string) {
	if path == "" || action == "" || permission == "" {
		return
	}
	extraPlayerCommandRoutes[path] = playerCommandRoute{action: action, permission: permission}
}

func registerPlayerSubresource(name, query string) {
	if name == "" || query == "" {
		return
	}
	extraPlayerSubresources[name] = query
}

func grantRolePermission(role, permission string) {
	if role == "" || permission == "" {
		return
	}
	if extraRolePermissions[role] == nil {
		extraRolePermissions[role] = map[string]bool{}
	}
	extraRolePermissions[role][permission] = true
}

func hasExtraPermission(role, permission string) bool {
	return extraRolePermissions[role][permission]
}
