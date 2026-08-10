package chat

import (
	"errors"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

func TestMapWSFrame(t *testing.T) {
	tests := []struct {
		name       string
		v          *dtochat.ChatSSEResp
		first      bool
		wantFlag   dtochat.WSFlag
		wantCode   int
		wantIsErr  bool
		wantHasSSE bool
	}{
		{
			name:       "first frame without usage maps to start",
			v:          &dtochat.ChatSSEResp{Chat: dtochat.Chat{ID: "c1"}},
			first:      true,
			wantFlag:   dtochat.WSFlagStart,
			wantCode:   dtocode.SystemSuccess.Code,
			wantIsErr:  false,
			wantHasSSE: true,
		},
		{
			name:       "delta frame without usage",
			v:          &dtochat.ChatSSEResp{Chat: dtochat.Chat{ID: "c1", Content: "hi"}},
			first:      false,
			wantFlag:   dtochat.WSFlagDelta,
			wantCode:   dtocode.SystemSuccess.Code,
			wantIsErr:  false,
			wantHasSSE: true,
		},
		{
			name: "done frame with usage",
			v: &dtochat.ChatSSEResp{
				Chat:  dtochat.Chat{ID: "c1", Content: "answer"},
				Usage: dtochat.Usage{TotalTokens: 42},
			},
			first:      false,
			wantFlag:   dtochat.WSFlagDone,
			wantCode:   dtocode.SystemSuccess.Code,
			wantIsErr:  false,
			wantHasSSE: true,
		},
		{
			name:       "error frame only carries flag",
			v:          &dtochat.ChatSSEResp{Err: errors.New("boom")},
			first:      false,
			wantFlag:   dtochat.WSFlagError,
			wantCode:   dtocode.ChatSSEFailure.Code,
			wantIsErr:  true,
			wantHasSSE: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, isErr := mapWSFrame(tt.v, tt.first)

			if isErr != tt.wantIsErr {
				t.Fatalf("isErr = %v, want %v", isErr, tt.wantIsErr)
			}
			if resp.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", resp.Code, tt.wantCode)
			}
			data, ok := resp.Data.(dtochat.ChatWSResp)
			if !ok {
				t.Fatalf("Data type = %T, want dtochat.ChatWSResp", resp.Data)
			}
			if data.Flag != tt.wantFlag {
				t.Errorf("Flag = %q, want %q", data.Flag, tt.wantFlag)
			}
			if tt.wantHasSSE && data.Data != *tt.v {
				t.Errorf("Data = %+v, want %+v", data.Data, *tt.v)
			}
			if !tt.wantHasSSE && data.Data != (dtochat.ChatSSEResp{}) {
				t.Errorf("Data = %+v, want zero ChatSSEResp", data.Data)
			}
		})
	}
}
