package core

import "sync"

// inflightSet tam doğrulaması (checkBody: imzalar, işlem kuralları) süren blokların özetleridir.
// Aynı blok aynı anda birden çok yoldan gelirse (iki eşten itme, itme ile uç beklemesi,
// senkronizasyon) imzalar bir kez doğrulanır; diğer çağrılar bitmesini bekleyip AcceptBlock'u
// baştan dener: blok kabul edildiyse ErrKnownBlock, gövdesi geçersizse ErrKnownInvalid alır.
//
// Anahtar olarak özet yeterlidir: kayıt checkHeader'dan sonra alınır ve orada gövde Merkle
// köküyle özete bağlanır (yapraklar imzayı da kapsayan işlem kimlikleridir). Aynı özetli farklı
// bir gövde kayda hiç ulaşamaz. Kendi kilidi vardır ve bc.mu tutulurken alınmaz.
type inflightSet struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

// retryNow kapalı bir kanaldır: AcceptBlock'un beklemeden baştan denemesini sağlar.
var retryNow = func() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// claim özeti doğrulamaya alır. Başka bir çağrı zaten doğruluyorsa busy onun bitişinde kapanan
// kanaldır. Değilse release, doğrulama bittiğinde (başarılı ya da değil) bir kez çağrılmalıdır.
func (s *inflightSet) claim(hash string) (release func(), busy <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.m[hash]; ok {
		return nil, c
	}
	if s.m == nil {
		s.m = map[string]chan struct{}{}
	}
	c := make(chan struct{})
	s.m[hash] = c
	return func() {
		s.mu.Lock()
		delete(s.m, hash)
		s.mu.Unlock()
		close(c)
	}, nil
}
