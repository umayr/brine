package web

import (
	"bytes"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/projectsnapshot"
	"sort"
)

func brokerInputNames(s projectsnapshot.Snapshot) []string {
	for _, f := range s.Files {
		if f.Path == brokerwire.InputsFile {
			i, err := brokerwire.DecodeInputs(bytes.NewReader(f.Content))
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(i.Variables))
			for k := range i.Variables {
				names = append(names, k)
			}
			sort.Strings(names)
			return names
		}
	}
	return nil
}
