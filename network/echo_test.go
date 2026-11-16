package network

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestPushSource(t *testing.T) {
	cases := []struct {
		remote string
		port   uint16
		want   string
	}{
		{"1.2.3.4:51234", 8001, "1.2.3.4:8001"},
		{"[2001:db8::1]:51234", 8001, "[2001:db8::1]:8001"},
		{"1.2.3.4:51234", 0, ""}, // eski düğüm ya da dinlemeyen gönderen
		{"bozuk", 8001, ""},
	}
	for _, c := range cases {
		if got := pushSource(c.remote, c.port); got != c.want {
			t.Fatalf("pushSource(%q, %d) = %q, beklenen %q", c.remote, c.port, got, c.want)
		}
	}
}

func TestSamePeer(t *testing.T) {
	same := [][2]string{
		{"1.2.3.4:8001", "1.2.3.4:8001"},
		{"[::ffff:1.2.3.4]:8001", "1.2.3.4:8001"},
		{"[2001:db8:0::1]:8001", "[2001:db8::1]:8001"},
		{"seed.example:8001", "seed.example:8001"},
	}
	for _, c := range same {
		if !samePeer(c[0], c[1]) {
			t.Fatalf("%s ile %s aynı eş sayılmadı", c[0], c[1])
		}
	}
	differ := [][2]string{
		{"1.2.3.4:8001", "1.2.3.4:8002"},
		{"1.2.3.4:8001", "1.2.3.5:8001"},
		{"seed.example:8001", "1.2.3.4:8001"}, // ad çözülmez
		{"bozuk", "1.2.3.4:8001"},
	}
	for _, c := range differ {
		if samePeer(c[0], c[1]) {
			t.Fatalf("%s ile %s aynı eş sayıldı", c[0], c[1])
		}
	}
}

// Dinleyen düğüm her mesaja dinleme portunu yazar; dinlemeyen yazmaz.
func TestStampListenPort(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	if p := ps.stamp(message{Type: msgInv}).Port; p != 0 {
		t.Fatalf("dinlemeyen düğüm port %d yazdı", p)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ps.syncEvery = time.Hour
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	want := uint16(ln.Addr().(*net.TCPAddr).Port)
	waitFor(t, "dinleme portu yazılmadı", func() bool { return ps.stamp(message{}).Port == want })
}

// echoRun alıcıya kaynak eşten bir blok iter (port bildirilen dinleme portudur, 0: bildirilmez)
// ve bloğun alıcının hangi eşlerine itildiğini döndürür. Kaynak alıcının yapılandırılmış eşidir.
func echoRun(t *testing.T, port uint16) map[string]bool {
	t.Helper()
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.syncEvery = time.Hour
	got := make(chan string, 4)
	ps.send = func(peer string, _ []byte, _ bool) error { got <- peer; return nil }
	source := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	if port == 0 {
		source = "127.0.0.1:1"
	}
	other := "127.0.0.2:1"
	ps.AddPeer(source) // önce: aynı yayında kaynağın kuyruğu diğerinden önce dolar
	ps.AddPeer(other)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	src := newChain(t)
	mineN(t, src, 1)
	blk, err := src.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	m := stamped(message{Type: msgBlock, Block: blk})
	m.Port = port
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	sent := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for !sent[other] {
		select {
		case p := <-got:
			sent[p] = true
		case <-deadline:
			t.Fatalf("blok diğer eşe itilmedi: %v", sent)
		}
	}
	waitFor(t, "kaynağın kuyruğu boşalmadı", ps.senderFor(source).idle)
	for len(got) > 0 {
		sent[<-got] = true
	}
	return sent
}

// Gelen bağlantıyla itilen blok, gönderen dinleme portunu bildirdiyse kaynağına geri itilmez;
// bildirmediyse (eski düğüm) eskisi gibi geri itilir.
func TestPushNotEchoed(t *testing.T) {
	if sent := echoRun(t, 28123); sent["127.0.0.1:28123"] {
		t.Fatalf("blok kaynağına geri itildi: %v", sent)
	}
	if sent := echoRun(t, 0); !sent["127.0.0.1:1"] {
		t.Fatalf("port bildirmeyen kaynağa blok itilmedi: %v", sent)
	}
}
