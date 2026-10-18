package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// nodeUpQuery lists every node-exporter target with its scrape health (1 = up).
const nodeUpQuery = `up{job="node"}`

// instanceError is a client error from choosing an instance, with its HTTP status.
type instanceError struct {
	status int
	msg    string
}

// selectInstance picks the item belonging to the wanted instance.
//   - No items: found=false and no error (no data yet).
//   - want == "": the only item, or a 400 listing the instances when there are several.
//   - want set: the matching item, or a 404 listing the instances.
func selectInstance[T any](items []T, instanceOf func(T) string, want string) (item T, found bool, ierr *instanceError) {
	if len(items) == 0 {
		return item, false, nil
	}
	if want == "" {
		if len(items) == 1 {
			return items[0], true, nil
		}
		return item, false, &instanceError{http.StatusBadRequest,
			"Multiple instances; pass instance= one of: " + instanceNames(items, instanceOf)}
	}
	for _, candidate := range items {
		if instanceOf(candidate) == want {
			return candidate, true, nil
		}
	}
	return item, false, &instanceError{http.StatusNotFound,
		fmt.Sprintf("Unknown instance %q; available: %s", want, instanceNames(items, instanceOf))}
}

func instanceNames[T any](items []T, instanceOf func(T) string) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, instanceOf(item))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func seriesInstance(s promSeries) string { return s.Instance }
func sampleInstance(s promSample) string { return s.Instance }

type instanceStatus struct {
	Instance string `json:"instance"`
	Up       bool   `json:"up"`
}

// instancesHandler lists the node-exporter targets Prometheus scrapes and whether each is up.
func instancesHandler(w http.ResponseWriter, r *http.Request) {
	samples, err := queryPrometheusInstant(nodeUpQuery)
	if err != nil {
		http.Error(w, "Failed to fetch instances: "+err.Error(), http.StatusInternalServerError)
		return
	}

	list := make([]instanceStatus, 0, len(samples))
	for _, s := range samples {
		list = append(list, instanceStatus{Instance: s.Instance, Up: s.Value == 1})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Instance < list[j].Instance })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]instanceStatus{"instances": list})
}
