package core

import (
	"errors"
	"testing"
)

// Derin reorg alarmı zincir güncellemesiyle diske yazılır, yeniden açılışta sürer ve yalnızca
// ClearDeepReorgAlarm ile (kalıcı olarak) temizlenir; ikinci derin reorg olay sayısını artırır.
func TestDeepReorgAlarmPersists(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	depth := int(testParams.ReorgAlarmDepth) + 1
	g := genesisOf(t, bc)
	main := extend(t, bc, g, depth, newKey(t).Address)
	mustAccept(t, bc, main...)
	side := extend(t, bc, g, depth+1, newKey(t).Address)
	mustAccept(t, bc, side...)
	if !bc.DeepReorgAlarm() {
		t.Fatal("derin reorg alarm vermedi")
	}
	bc.Close()

	reopen := func() *Blockchain {
		t.Helper()
		b, err := NewBlockchain(dir, testParams)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}
	bc2 := reopen()
	if !bc2.DeepReorgAlarm() {
		t.Fatal("alarm yeniden açılışta kayboldu")
	}
	if c, err := bc2.ConfirmationOf(side[0].Hash); err != nil || !c.DeepReorg {
		t.Fatalf("onay bilgisi alarmı göstermiyor: %+v, %v", c, err)
	}
	rec, err := bc2.ClearDeepReorgAlarm()
	if err != nil || rec == nil || rec.Depth != int64(depth) || rec.ForkHeight != 0 || rec.Count != 1 ||
		rec.OldTip != main[depth-1].Hash || rec.NewTip != side[depth].Hash {
		t.Fatalf("temizlenen kayıt %+v, %v", rec, err)
	}
	if bc2.DeepReorgAlarm() {
		t.Fatal("alarm temizlenmedi")
	}
	if rec, err := bc2.ClearDeepReorgAlarm(); rec != nil || err != nil {
		t.Fatalf("alarm yokken temizleme %+v, %v", rec, err)
	}
	bc2.Close()

	bc3 := reopen()
	if bc3.DeepReorgAlarm() {
		t.Fatal("temizlenen alarm yeniden açılışta geri geldi")
	}
}

// Temizlenmeden gelen ikinci derin reorg kaydı günceller ve olay sayısını artırır.
func TestDeepReorgAlarmCounts(t *testing.T) {
	bc := newTestChain(t)
	depth := int(testParams.ReorgAlarmDepth) + 1
	g := genesisOf(t, bc)
	a := extend(t, bc, g, depth, newKey(t).Address)
	mustAccept(t, bc, a...)
	b := extend(t, bc, g, depth+1, newKey(t).Address)
	mustAccept(t, bc, b...)
	c := extend(t, bc, g, depth+2, newKey(t).Address)
	mustAccept(t, bc, c...)
	if bc.deepReorg == nil || bc.deepReorg.Count != 2 || bc.deepReorg.Depth != int64(depth+1) ||
		bc.deepReorg.NewTip != c[depth+1].Hash {
		t.Fatalf("kayıt %+v", bc.deepReorg)
	}
}

// Alarm derinliği blok süresinden türetilir (~2 saat) ve coinbase olgunluğundan küçük olmalıdır.
func TestReorgAlarmDepthParams(t *testing.T) {
	if TestnetParams.ReorgAlarmDepth != 120 || TestnetParams.ReorgAlarmDepth*TestnetParams.TargetSpacing != 2*3600 {
		t.Fatalf("testnet alarm derinliği %d", TestnetParams.ReorgAlarmDepth)
	}
	half := *TestnetParams
	half.TargetSpacing = 30
	withEmission(&half)
	if half.ReorgAlarmDepth != 240 || half.CoinbaseMaturity != 480 {
		t.Fatalf("30 sn'de alarm %d, olgunluk %d", half.ReorgAlarmDepth, half.CoinbaseMaturity)
	}
	for _, d := range []int64{0, TestnetParams.CoinbaseMaturity} {
		p := *TestnetParams
		p.ReorgAlarmDepth = d
		if err := p.Validate(); !errors.Is(err, ErrBadParams) {
			t.Fatalf("alarm derinliği %d kabul edildi: %v", d, err)
		}
	}
}
