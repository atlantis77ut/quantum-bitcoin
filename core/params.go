package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"quantum-bitcoin/crypto"
)

// Params bir ağın uzlaşma kimliğidir: ad, başlangıç hedefi ve genesis bloğu.
// Genesis değerleri sabittir ve GenesisHash testlerde bağımsız (Python) vektörlerle sabitlenir.
type Params struct {
	Name             string
	Bits             uint32 // başlangıç hedefi (yeniden hedefleme ayrı bir PR'da)
	GenesisTimestamp int64
	GenesisMessage   string // genesis'in bu andan önce bilinemeyeceğini gösteren taahhüt
	GenesisNonce     uint64
	GenesisHash      string // boşsa ağ henüz başlatılamaz (lansman öncesi mainnet)
}

// sigTag imza alanının sürüm etiketidir.
const sigTag = "QBTC-SIG-v3"

var (
	// MainnetParams: genesis kasıtlı olarak tanımsızdır. Genesis bugünden sabitlenirse onu bilen
	// herkes lansmana kadar gizlice zincir kazıp "premine yok" vaadini delebilir. Mainnet genesis'i
	// lansman anında öngörülemez bir mesajla (ör. o anki bir Bitcoin blok özeti) üretilecektir.
	MainnetParams = &Params{Name: "mainnet", Bits: 0x1f00ffff}

	// TestnetParams: herkese açık test ağı (hedef eski zorluk 4'e eşdeğer).
	TestnetParams = &Params{
		Name:             "testnet",
		Bits:             0x1f00ffff,
		GenesisTimestamp: 1794096000, // 2026-11-08T00:00:00Z
		GenesisMessage:   "Q-BTC testnet genesis 2026-11-08",
		GenesisNonce:     129686,
		GenesisHash:      "00004fb2d4e92c6947ed1d8b9c039fd4ea0d23f2dd48babeeded9082f7e6b0ae",
	}

	// RegtestParams: yerel geliştirme ve testler için en kolay hedef (powLimit'e yakın).
	RegtestParams = &Params{
		Name:             "regtest",
		Bits:             0x200fffff,
		GenesisTimestamp: 1794096000,
		GenesisMessage:   "Q-BTC regtest genesis",
		GenesisNonce:     6,
		GenesisHash:      "02f739de53b008231b18af8ef6c028edf6f6f80e560895994577bfbdf9986157",
	}
)

// ParamsByName komut satırındaki ağ adını parametrelere çevirir.
func ParamsByName(name string) (*Params, error) {
	for _, p := range []*Params{MainnetParams, TestnetParams, RegtestParams} {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("bilinmeyen ağ: %q (mainnet, testnet, regtest)", name)
}

// SigDomain imzaları bu ağa ve genesis'ine bağlayan alan etiketidir.
func (p *Params) SigDomain() (string, error) {
	if p == nil || p.GenesisHash == "" {
		return "", ErrNoGenesis
	}
	return sigTag + "|" + p.Name + "|" + p.GenesisHash, nil
}

// GenesisReceiver genesis coinbase'inin alıcısıdır: mesajın özetinden türetilmiş, biçimce
// geçerli bir adres. Genesis tutarı sıfır olduğundan bu adresin harcanabilirliği önemsizdir.
func (p *Params) GenesisReceiver() string {
	h := sha256.Sum256([]byte(p.GenesisMessage))
	return crypto.AddressPrefix + hex.EncodeToString(h[:20])
}
