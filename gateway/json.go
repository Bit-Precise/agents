package gateway

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
