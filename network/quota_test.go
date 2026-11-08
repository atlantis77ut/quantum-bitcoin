package network

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestPeerGroup(t *testing.T) {
	if peerGroup("203.0.113.7") != peerGroup("203.0.113.200") {
		t.Fatal("aynı /24 farklı gruplarda")
	}
	if peerGroup("203.0.113.7") == peerGroup("203.0.114.7") {
		t.Fatal("farklı /24 aynı grupta")
	}
	if peerGroup("2001:db8:1:2::1") != peerGroup("2001:db8:1:ffff::9") {
		t.Fatal("aynı /48 farklı gruplarda")
	}
	if peerGroup("2001:db8:1::1") == peerGroup("2001:db8:2::1") {
		t.Fatal("farklı /48 aynı grupta")
	}
	// IPv4-eşlemeli IPv6 adresi IPv4 grubuna düşer.
	if peerGroup("::ffff:203.0.113.7") != peerGroup("203.0.113.9") {
		t.Fatal("IPv4-eşlemeli adres ayrı grupta")
	}
}

func TestGroupQuotaWait(t *testing.T) {
	q := newGroupQuota(1)
	if !q.tryAcquire("g") {
		t.Fatal("ilk yuva alınamadı")
	}
	start := time.Now()
	if q.acquire("g", 100*time.Millisecond) {
		t.Fatal("dolu kota alındı")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("kota beklemeden reddetti")
	}
	go func() { time.Sleep(50 * time.Millisecond); q.release("g") }()
	if !q.acquire("g", 2*time.Second) {
		t.Fatal("boşalan yuva beklenirken alınamadı")
	}
	q.release("g")
	if len(q.counts) != 0 {
		t.Fatalf("serbest bırakılan grup tabloda kaldı: %v", q.counts)
	}
}

// Yanıtı okumayı bırakan eş yazarı (ve sunum yuvasını) toplam süre boyunca değil, en çok
// bir parça süresi kadar tutar.
func TestDeadlineWriterStalledReader(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		buf := make([]byte, 1024)
		io.ReadFull(b, buf) // ilk parçayı oku, sonra dur
	}()
	w := &deadlineWriter{conn: a, chunk: 1024, per: 100 * time.Millisecond, end: time.Now().Add(time.Minute)}
	start := time.Now()
	n, err := w.Write(make([]byte, 8*1024))
	if err == nil {
		t.Fatal("okumayan eşe yazım bitti")
	}
	if n != 1024 {
		t.Fatalf("yazılan %d bayt, beklenen 1024", n)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("yazar %v boyunca bekledi", d)
	}
}

func TestDeadlineWriterDeliversAll(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	want := bytes.Repeat([]byte("qbtc"), 5000)
	got := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(b)
		got <- data
	}()
	w := &deadlineWriter{conn: a, chunk: 1000, per: time.Second, end: time.Now().Add(10 * time.Second)}
	if n, err := w.Write(want); err != nil || n != len(want) {
		t.Fatalf("yazım: %d %v", n, err)
	}
	a.Close()
	if data := <-got; !bytes.Equal(data, want) {
		t.Fatalf("okunan %d bayt, beklenen %d", len(data), len(want))
	}
}

// Tek bir /48 farklı /64'lerle tabloyu şişiremez: kota dışı /64'ler önekin ortak kovasını paylaşır.
func TestRateLimitPrefixCap(t *testing.T) {
	l := newIPRateLimiter(1, 2)
	l.perPrefix = 2
	at := time.Unix(1_900_000_000, 0)
	l.now = func() time.Time { return at }
	for _, h := range []string{"2001:db8:1:1::1", "2001:db8:1:2::1"} {
		if !l.allowHost(h) {
			t.Fatalf("%s reddedildi", h)
		}
	}
	// Üçüncü ve sonraki /64'ler önekin ortak kovasını (burst 2) paylaşır.
	if !l.allowHost("2001:db8:1:3::1") || !l.allowHost("2001:db8:1:4::1") {
		t.Fatal("ortak kova ilk istekleri reddetti")
	}
	if l.allowHost("2001:db8:1:5::1") {
		t.Fatal("ortak kova tükenmedi")
	}
	if len(l.buckets) != 3 {
		t.Fatalf("tabloda %d kayıt, beklenen 3", len(l.buckets))
	}
	// Kotadaki /64'ler ve başka bir /48 etkilenmez.
	if !l.allowHost("2001:db8:1:1::9") || !l.allowHost("2001:db8:2::1") {
		t.Fatal("kotadaki /64 ya da başka /48 etkilendi")
	}
	// Boşta kalan kayıtlar temizlenince önek sayacı da düşer.
	at = at.Add(10 * time.Minute)
	l.allowHost("192.0.2.1")
	if len(l.prefixes) != 0 || len(l.buckets) != 1 {
		t.Fatalf("temizlik sonrası önekler %v, kayıt %d", l.prefixes, len(l.buckets))
	}
}

// Tablo doluyken yeni istemciler reddedilmez, ortak taşma kovasını paylaşır; mevcut
// istemciler etkilenmez.
func TestRateLimitOverflow(t *testing.T) {
	l := newIPRateLimiter(1, 2)
	l.maxKeys = 2
	at := time.Unix(1_900_000_000, 0)
	l.now = func() time.Time { return at }
	if !l.allowHost("192.0.2.1") || !l.allowHost("192.0.2.2") {
		t.Fatal("ilk istemciler reddedildi")
	}
	allowed := 0
	for i := 0; i < 100; i++ {
		if l.allowHost("198.51.100.7") {
			allowed++
		}
	}
	if allowed != 2*overflowFactor {
		t.Fatalf("taşma kovası %d istek geçirdi, beklenen %d", allowed, 2*overflowFactor)
	}
	if !l.allowHost("192.0.2.1") {
		t.Fatal("mevcut istemci taşmadan etkilendi")
	}
	if len(l.buckets) != 2 {
		t.Fatalf("tablo sınırı aşıldı: %d", len(l.buckets))
	}
}
