package har

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
)

func TestBuild(t *testing.T) {
	d := model.CaptureFlowDetail{
		Flow: model.CaptureFlow{Start: time.Now(), Method: "POST", Scheme: "https", Host: "a.test", Port: 443, Path: "/p?token=abcdefghijk&x=1",
			Status: 200, ContentType: "application/json", Proto: "h2", Flags: []string{model.FlowDecrypted}, Process: "curl"},
		ReqHeader:  []model.KV{{Key: "authorization", Value: "Bearer secretsecretsecret"}, {Key: "content-type", Value: "application/json"}},
		RespHeader: []model.KV{{Key: "content-type", Value: "application/json"}},
		ReqBody:    []byte(`{"password":"pppppppppp","q":1}`), ReqBodySize: 31,
		RespBody: []byte(`{"ok":true}`), RespBodySize: 11,
	}
	pass := model.CaptureFlowDetail{Flow: model.CaptureFlow{Host: "b.test", Flags: []string{model.FlowPassthrough}}}
	for _, bodies := range []bool{false, true} {
		b, err := Build([]model.CaptureFlowDetail{d, pass}, bodies, "test")
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		var doc struct {
			Log struct {
				Entries []map[string]any
			}
		}
		if err := json.Unmarshal(b, &doc); err != nil || len(doc.Log.Entries) != 1 {
			t.Fatalf("%v %d", err, len(doc.Log.Entries))
		}
		for _, secret := range []string{"secretsecretsecret", "abcdefghijk", "pppppppppp"} {
			if strings.Contains(s, secret) {
				t.Fatalf("bodies=%v: %s leaked", bodies, secret)
			}
		}
		if strings.Contains(s, `\"ok\": true`) != bodies || !strings.Contains(s, `"url": "https://a.test/p?token=`) || !strings.Contains(s, `"_lanflow"`) {
			t.Fatalf("bodies=%v:\n%s", bodies, s)
		}
	}
}
