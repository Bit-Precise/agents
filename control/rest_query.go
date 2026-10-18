package control

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/bit-precise/agents/control/types"
)

type pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}
type resourceCollection struct {
	Items      []any      `json:"items"`
	Pagination pagination `json:"pagination"`
}
type querySpec struct{ filters, sorts []string }

var resourceQueries = map[string]querySpec{
	"users":            {[]string{"q", "disabled", "admin", "workspaceId"}, []string{"id", "name", "createdAt"}},
	"workspaces":       {[]string{"q", "userId", "ownerId"}, []string{"id", "name", "createdAt"}},
	"memberships":      {[]string{"workspaceId", "userId", "role"}, []string{"id", "workspaceId", "userId", "role"}},
	"agents":           {[]string{"workspaceId", "principalType", "principalId", "createdBy", "status", "q"}, []string{"id", "name", "createdAt", "status"}},
	"service-accounts": {[]string{"workspaceId", "status", "createdBy", "q"}, []string{"id", "name", "createdAt", "status"}},
	"ingresses":        {[]string{"workspaceId", "agentId", "status", "enabled", "q"}, []string{"id", "name", "status"}},
	"egresses":         {[]string{"workspaceId", "agentId", "status", "enabled", "q"}, []string{"id", "name", "status"}},
	"enrollment-keys":  {[]string{"workspaceId", "serviceAccountId", "userId", "createdBy", "revoked"}, []string{"id", "expiresAt"}},
	"servers":          {[]string{"workspaceId", "agentId", "enabled", "q"}, []string{"id", "name", "hostname"}},
	"routes":           {[]string{"workspaceId", "agentId", "kind", "enabled", "q"}, []string{"id", "name", "kind", "priority"}},
	"grants":           {[]string{"workspaceId", "resourceType", "resourceId", "subjectType", "subjectId", "revoked"}, []string{"id", "startsAt", "expiresAt"}},
	"audit-events":     {[]string{"actorId", "resourceId", "action"}, []string{"id", "at", "action"}},
	"connections":      {[]string{"workspaceId", "agentId", "role"}, []string{"id", "connectedAt", "role"}},
	"flows":            {[]string{"workspaceId", "agentId", "consumerType", "consumerId", "resourceId", "purpose"}, []string{"id", "startedAt", "state", "purpose"}},
}

func invalidQuery(message string) error {
	return &types.Error{Kind: types.KindInvalid, Message: message}
}
func resourcePage(r *http.Request, resource string, rows []resourceRow, snap types.Snapshot) (resourceCollection, error) {
	spec := resourceQueries[resource]
	query, err := parseResourceQuery(r, spec, resource)
	if err != nil {
		return resourceCollection{}, err
	}
	page, pageSize := 1, 20
	if query["page"] != "" {
		page, _ = strconv.Atoi(query["page"])
	}
	if query["pageSize"] != "" {
		pageSize, _ = strconv.Atoi(query["pageSize"])
	}
	filtered := make([]resourceRow, 0, len(rows))
	for _, item := range rows {
		match := true
		for _, key := range spec.filters {
			value, ok := query[key]
			if !ok {
				continue
			}
			switch {
			case key == "q":
				match = strings.Contains(item.search, strings.ToLower(value))
			case resource == "users" && key == "workspaceId":
				match = membershipMatches(snap, value, item.id, "")
			case resource == "workspaces" && key == "userId":
				match = membershipMatches(snap, item.id, value, "")
			case resource == "workspaces" && key == "ownerId":
				match = membershipMatches(snap, item.id, value, "owner")
			default:
				match = item.fields[key] == value
			}
			if !match {
				break
			}
		}
		if match {
			filtered = append(filtered, item)
		}
	}
	sortBy, sortOrder := query["sortBy"], query["sortOrder"]
	if sortBy == "" {
		sortBy = "id"
		if resource == "audit-events" {
			sortBy = "at"
		}
	}
	if sortOrder == "" {
		sortOrder = "asc"
		if resource == "audit-events" {
			sortOrder = "desc"
		}
	}
	slices.SortFunc(filtered, func(x, y resourceRow) int {
		comparison := strings.Compare(x.fields[sortBy], y.fields[sortBy])
		if sortBy == "priority" {
			a, _ := strconv.Atoi(x.fields[sortBy])
			b, _ := strconv.Atoi(y.fields[sortBy])
			comparison = 0
			if a < b {
				comparison = -1
			}
			if a > b {
				comparison = 1
			}
		}
		if comparison == 0 {
			comparison = strings.Compare(x.id, y.id)
		}
		if sortOrder == "desc" {
			comparison = -comparison
		}
		return comparison
	})
	total := len(filtered)
	pages := (total + pageSize - 1) / pageSize
	out := resourceCollection{Items: []any{}, Pagination: pagination{page, pageSize, total, pages}}
	// Compare before multiplying, so even MaxInt page requests cannot overflow.
	if page > pages {
		return out, nil
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	for _, v := range filtered[start:end] {
		out.Items = append(out.Items, v.value)
	}
	return out, nil
}
func parseResourceQuery(r *http.Request, spec querySpec, resource string) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidQuery("invalid URL query")
	}
	out := map[string]string{}
	for key, entries := range values {
		if len(entries) != 1 {
			return nil, invalidQuery("query parameters must not repeat")
		}
		value := entries[0]
		if !slices.Contains(spec.filters, key) && !slices.Contains([]string{"page", "pageSize", "sortBy", "sortOrder"}, key) {
			return nil, invalidQuery("unsupported query parameter: " + key)
		}
		if value == "" && key != "q" {
			return nil, invalidQuery("empty query parameter: " + key)
		}
		switch key {
		case "page", "pageSize":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || (key == "pageSize" && n > 100) {
				return nil, invalidQuery("page must be positive and pageSize must be between 1 and 100")
			}
		case "sortBy":
			if !slices.Contains(spec.sorts, value) {
				return nil, invalidQuery("unsupported sort field")
			}
		case "sortOrder":
			if value != "asc" && value != "desc" {
				return nil, invalidQuery("sortOrder must be asc or desc")
			}
		case "disabled", "admin", "enabled", "revoked":
			if value != "true" && value != "false" {
				return nil, invalidQuery(key + " must be true or false")
			}
		case "status":
			if !slices.Contains([]string{"active", "revoked"}, value) {
				return nil, invalidQuery("invalid status")
			}
		case "kind":
			if !slices.Contains([]string{"serve", "egress"}, value) {
				return nil, invalidQuery("invalid route kind")
			}
		case "resourceType":
			if !slices.Contains([]string{"service", "egress"}, value) {
				return nil, invalidQuery("invalid resource type")
			}
		case "subjectType":
			if !slices.Contains([]string{"user", "workspace", "anonymous"}, value) {
				return nil, invalidQuery("invalid subject type")
			}
		case "purpose":
			if !slices.Contains([]string{"serve", "egress"}, value) {
				return nil, invalidQuery("invalid flow purpose")
			}
		case "role":
			roles := []string{"owner", "maintainer", "developer", "guest"}
			if resource == "connections" {
				roles = []string{"provider", "access"}
			}
			if !slices.Contains(roles, value) {
				return nil, invalidQuery("invalid role")
			}
		}
		out[key] = value
	}
	return out, nil
}
