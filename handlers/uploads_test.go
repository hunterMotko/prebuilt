package handlers

import (
	"mime/multipart"
	"testing"
)

func fakeFiles(n int) []*multipart.FileHeader {
	files := make([]*multipart.FileHeader, n)
	for i := range files {
		files[i] = &multipart.FileHeader{Filename: "test.jpg"}
	}
	return files
}

func TestCheckPhotoCap(t *testing.T) {
	cases := []struct {
		name          string
		existingCount int
		byCategory    map[string][]*multipart.FileHeader
		wantErr       bool
	}{
		{
			name:          "well under the cap",
			existingCount: 0,
			byCategory:    map[string][]*multipart.FileHeader{"exterior": fakeFiles(5)},
			wantErr:       false,
		},
		{
			name:          "exactly at the cap is allowed",
			existingCount: 0,
			byCategory:    map[string][]*multipart.FileHeader{"exterior": fakeFiles(12)},
			wantErr:       false,
		},
		{
			name:          "one over the cap is rejected",
			existingCount: 0,
			byCategory:    map[string][]*multipart.FileHeader{"exterior": fakeFiles(13)},
			wantErr:       true,
		},
		{
			name:          "existing plus new pushes over the cap",
			existingCount: 10,
			byCategory:    map[string][]*multipart.FileHeader{"interior": fakeFiles(3)},
			wantErr:       true,
		},
		{
			name:          "already at the cap, adding zero more is fine",
			existingCount: 12,
			byCategory:    map[string][]*multipart.FileHeader{},
			wantErr:       false,
		},
		{
			name:          "counted across multiple categories in one request",
			existingCount: 0,
			byCategory: map[string][]*multipart.FileHeader{
				"exterior": fakeFiles(5),
				"interior": fakeFiles(4),
				"feature":  fakeFiles(3),
			},
			wantErr: false, // 5+4+3 = 12, exactly at the cap
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPhotoCap(tc.existingCount, tc.byCategory)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkPhotoCap(%d, ...) error = %v, wantErr %v", tc.existingCount, err, tc.wantErr)
			}
		})
	}
}
