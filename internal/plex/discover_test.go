package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// A fake plex.tv whose token reaches an owned server and a friend's shared one: only the
// owned server comes back, its relay is dropped and its connections are in try-order.
func TestDiscoverServersOwnedOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/resources" || r.Header.Get("X-Plex-Token") != "tok" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"name":"Friend's","provides":"server","clientIdentifier":"F1","owned":false,
			 "connections":[{"uri":"http://10.0.0.9:32400","address":"10.0.0.9","port":32400,"protocol":"http","local":true}]},
			{"name":"Phone","provides":"player","clientIdentifier":"P1","owned":true,"connections":[]},
			{"name":"Home","provides":"server","clientIdentifier":"M1","owned":true,
			 "connections":[
				{"uri":"https://1-2-3-4.abc.plex.direct:32400","address":"1.2.3.4","port":32400,"protocol":"https","local":false},
				{"uri":"https://relay.plex.direct:8443","address":"5.6.7.8","port":8443,"protocol":"https","local":false,"relay":true},
				{"uri":"https://192-168-1-10.abc.plex.direct:32400","address":"192.168.1.10","port":32400,"protocol":"https","local":true},
				{"uri":"http://192.168.1.10:32400","address":"192.168.1.10","port":32400,"protocol":"http","local":true}
			 ]}
		]`))
	}))
	defer srv.Close()
	defer SetTVBaseForTest(srv.URL)()

	got, err := DiscoverServers(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MachineID != "M1" || got[0].Name != "Home" || !got[0].Owned {
		t.Fatalf("servers = %+v, want only the owned Home server", got)
	}
	var urls []string
	for _, c := range got[0].Conns {
		urls = append(urls, c.URL())
	}
	want := []string{"http://192.168.1.10:32400", "https://192-168-1-10.abc.plex.direct:32400", "https://1-2-3-4.abc.plex.direct:32400"}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("connection order = %v, want %v", urls, want)
	}
}
