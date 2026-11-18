package core

import (
	"errors"
	"testing"
)

// Eksik veri alarmı daldaki bir gövdeyi gösterirken geçiş denemesi önce yalnızca o gövdeyi
// yoklar: ilk deneme önekin gövdelerini okuyup bozuk blokta düşer, sonraki denemeler (yeniden
// gönderim ya da dalın başka bir ucu) yalnızca bozuk gövdeyi okur, alarm yeni olay saymaz.
// Gövde düzelince tam deneme yapılır ve zincire geçilir; alarm daldaki bir bloğu göstermiyorsa
// yoklama yapılmaz.
func TestProbeAlarmBody(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	m1 := blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, m1)
	side := extend(t, bc, g, 6, newKey(t).Address) // s1-s2 sağlam, s3 bozulacak
	bc.mu.Lock()
	for _, b := range side {
		storeOnly(t, bc, b) // zincire geçmeden saklanır
	}
	bc.mu.Unlock()
	saved := rewriteBody(t, bc, side[2].Hash, func(b *Block) { b.Transactions[0].Amount++ })

	attempt := func(b *Block, wantLoads int64) {
		t.Helper()
		before := bc.bodyLoads.Load()
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrBlockDataMissing) {
			t.Fatalf("blok %d: %v, beklenen bozuk gövde", b.Index, err)
		}
		if n := bc.bodyLoads.Load() - before; n != wantLoads {
			t.Fatalf("blok %d: %d gövde okundu, beklenen %d", b.Index, n, wantLoads)
		}
		if a := bc.dataAlarm; a == nil || a.Hash != side[2].Hash || a.Kind != dataCorruptBody || a.Count != 1 {
			t.Fatalf("alarm kaydı %+v", a)
		}
	}
	attempt(side[5], 3) // alarm yok: s1, s2 ve bozuk s3 okunur
	attempt(side[5], 1) // yeniden gönderim: yalnızca s3
	attempt(side[4], 1) // dalın başka bir ucu: yalnızca s3
	if tipHash(t, bc) != m1.Hash || bc.behind != side[5].Hash {
		t.Fatalf("uç %s, kayıt %s", tipHash(t, bc), bc.behind)
	}

	// Alarmdaki blok dalda değil: yoklama yapılmaz, önek bağlanır.
	before := bc.bodyLoads.Load()
	if err := bc.AcceptBlock(side[1]); err != nil || tipHash(t, bc) != side[1].Hash {
		t.Fatalf("sağlam öneke geçilmedi: %v", err)
	}
	if n := bc.bodyLoads.Load() - before; n != 2 {
		t.Fatalf("önek için %d gövde okundu, beklenen 2", n)
	}

	// Onarım: yoklama geçer, tam deneme dalın kalanını bağlar (s3 bir kez fazla okunur).
	if err := bc.db.Put([]byte(side[2].Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	before = bc.bodyLoads.Load()
	if err := bc.AcceptBlock(side[5]); err != nil {
		t.Fatalf("onarımdan sonra: %v", err)
	}
	if n := bc.bodyLoads.Load() - before; n != 5 {
		t.Fatalf("onarımdan sonra %d gövde okundu, beklenen 5 (yoklama + s3-s6)", n)
	}
	if tipHash(t, bc) != side[5].Hash || bc.behind != "" || bc.BehindBlocks() != 0 {
		t.Fatalf("uç %s, kayıt %s", tipHash(t, bc), bc.behind)
	}
	noInvalid(t, bc)
	assertSameState(t, bc, replay(t, bc, side...))
}
