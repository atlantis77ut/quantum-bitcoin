package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/gpu"
	"quantum-bitcoin/network"
)

// mempoolSaveInterval havuzun düzenli kayıt aralığıdır.
const mempoolSaveInterval = 10 * time.Minute

func main() {
	fmt.Println("=== QUANTUM BITCOIN (Q-BTC) PRODUCTION NODE ===")

	// 1. Komut satırı parametreleri
	port := flag.String("port", ":8001", "P2P dinleme portu")
	apiPort := flag.String("api", ":9001", "Borsa API portu")
	dbPath := flag.String("db", "", "LevelDB klasör yolu (boşsa qbtc_<ağ>_db)")
	netName := flag.String("network", "testnet", "Ağ: testnet, regtest (mainnet genesis'i lansmanda tanımlanacak)")
	peerAddr := flag.String("peer", "", "Bağlanılacak diğer düğümlerin adresleri (virgülle ayrılmış)")
	tlsCert := flag.String("tls-cert", "", "API için TLS sertifika dosyası (boşsa HTTP)")
	tlsKey := flag.String("tls-key", "", "API için TLS anahtar dosyası")
	mineTo := flag.String("mine", "", "Kazım ödüllerinin gideceği adres (boşsa kazım yapılmaz)")
	clearAlarm := flag.Bool("clear-reorg-alarm", false, "Kalıcı derin reorg alarmını temizler ve çıkar (olay incelendikten sonra)")
	mineThreads := flag.Int("mine-threads", 0, "Kazım iş parçacığı sayısı (0: tüm çekirdekler)")
	benchHashrate := flag.Duration("bench-hashrate", 0, "Q-BTC başlığında hashrate'i bu süre ölçer, yazdırır ve çıkar (ör. 30s)")
	mineGPU := flag.Bool("mine-gpu", false, "Kazımı OpenCL aygıtında yapar (-tags opencl ile derlenmiş olmalı)")
	gpuDevice := flag.Int("gpu-device", 0, "Kazım için OpenCL aygıtı (-gpu-list ile listelenir)")
	gpuBatch := flag.Uint64("gpu-batch", 0, "GPU turundaki nonce sayısı (0: ~100 ms'ye ölçülerek seçilir)")
	gpuList := flag.Bool("gpu-list", false, "OpenCL aygıtlarını listeler ve çıkar")
	benchRuns := flag.Int("bench-runs", 1, "-bench-hashrate ölçüm sayısı; 1'den büyükse önce bir ısınma ölçümü atılır, ortanca raporlanır")
	benchOut := flag.String("bench-out", "", "-bench-hashrate raporunun yazılacağı JSON dosyası (docs/GPU-OLCUM.md)")
	benchAllowNonGPU := flag.Bool("bench-allow-non-gpu", false, "GPU ölçümünde GPU olmayan OpenCL aygıtına izin verir (yalnızca CI/pocl; sonuç GPU hashrate'i değildir)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if (*tlsCert == "") != (*tlsKey == "") {
		slog.Error("tls-cert ve tls-key birlikte verilmeli")
		os.Exit(2)
	}

	// 2. Blockchain
	params, err := core.ParamsByName(*netName)
	if err != nil {
		slog.Error("ağ seçilemedi", "err", err)
		os.Exit(2)
	}
	threads := *mineThreads
	if threads == 0 {
		threads = core.DefaultMineWorkers()
	}
	if threads < 1 || threads > core.MaxMineWorkers {
		slog.Error("geçersiz kazım iş parçacığı sayısı", "mine-threads", *mineThreads, "en_cok", core.MaxMineWorkers)
		os.Exit(2)
	}
	if *gpuList {
		devs, err := gpu.Devices()
		if err != nil {
			slog.Error("OpenCL aygıtları listelenemedi", "err", err)
			os.Exit(2)
		}
		if len(devs) == 0 {
			fmt.Println("OpenCL aygıtı yok")
		}
		for _, d := range devs {
			fmt.Printf("%d\t%s\t%s\t%s\tsürücü %s\n", d.Index, d.Type, d.Name, d.Platform, d.Driver)
		}
		return
	}
	var backend gpu.Backend
	var benchDev *gpu.Device
	if *mineGPU && *benchHashrate != 0 {
		// Aygıt türü açılmadan önce denetlenir: GPU olmayan aygıtın sonucu GPU hashrate'i değildir.
		devs, err := gpu.Devices()
		if err != nil {
			slog.Error("OpenCL aygıtları listelenemedi", "err", err)
			os.Exit(2)
		}
		d, err := benchDevice(devs, *gpuDevice, *benchAllowNonGPU)
		if err != nil {
			slog.Error("ölçüm başlatılamadı", "err", err, "ipucu", "-gpu-list ile aygıtları görün")
			os.Exit(2)
		}
		benchDev = &d
	}
	if *mineGPU {
		backend, err = gpu.Open(*gpuDevice, *gpuBatch)
		if err != nil {
			slog.Error("GPU kazıcısı açılamadı", "err", err)
			os.Exit(2)
		}
		defer backend.Close()
		slog.Info("GPU kazıcısı hazır", "aygit", backend.Name(), "tur", backend.RoundSize())
	}
	if *benchHashrate != 0 {
		// Ölçüm kazımla aynı turları kullanır; zincir açılmaz. Lansmanda açıklanan hashrate
		// aynı komutla yeniden üretilebilir (docs/MAINNET-ONCESI.md madde 6a).
		measure := func() (float64, error) { return core.MeasureHashrate(threads, *benchHashrate) }
		var round uint64
		if backend != nil {
			measure = func() (float64, error) { return core.MeasureBackendHashrate(backend, *benchHashrate) }
			round = backend.RoundSize()
		}
		rates, warmup, err := runBench(measure, *benchRuns)
		if err == nil {
			var rep benchReport
			rep, err = newBenchReport(params, benchDev, *benchAllowNonGPU, threads, round, *benchHashrate, rates, warmup)
			if err == nil {
				slog.Info("hashrate ölçümü", "network", rep.Network, "arka_uc", rep.Backend, "sure", benchHashrate.String(),
					"olcum", len(rates), "ortanca_hash_per_sn", rep.Median, "en_dusuk", rep.Min, "en_yuksek", rep.Max,
					"yayilim_yuzde", rep.SpreadPct, "tur", round, "is_parcacigi", threads,
					"bu_hashrate_icin_bits", rep.NetworkBits, "mainnet_baslangic_bits", rep.StartBits,
					"mainnet_taban_bits", rep.FloorBits)
				if *benchOut != "" {
					err = writeBenchReport(*benchOut, rep)
				}
			}
		}
		if err != nil {
			slog.Error("hashrate ölçülemedi", "err", err)
			backendClose(backend)
			os.Exit(2)
		}
		return
	}
	if backend != nil && *mineTo == "" {
		slog.Warn("-mine-gpu verildi ama -mine yok: kazım yapılmayacak")
	}
	if *dbPath == "" {
		*dbPath = "qbtc_" + params.Name + "_db"
	}
	bc, err := core.NewBlockchain(*dbPath, params)
	if errors.Is(err, core.ErrNoGenesis) {
		slog.Error("bu ağ henüz başlatılamaz: genesis lansmanda tanımlanacak", "network", params.Name)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("zincir başlatılamadı", "err", err)
		os.Exit(1)
	}
	defer bc.Close()
	if err := bc.SetMineWorkers(threads); err != nil {
		slog.Error("kazım iş parçacığı sayısı ayarlanamadı", "err", err)
		os.Exit(2)
	}
	if backend != nil {
		bc.SetMineBackend(backend)
	}
	if *clearAlarm {
		rec, err := bc.ClearDeepReorgAlarm()
		if err != nil {
			slog.Error("derin reorg alarmı temizlenemedi", "err", err)
			os.Exit(1)
		}
		if rec == nil {
			slog.Info("temizlenecek derin reorg alarmı yok")
		}
		// Düğüm çalışmaya devam etmez: bayrak servis yapılandırmasında kalırsa her yeniden
		// başlatma (açılıştaki kurtarmanın yeni alarmı dahil) alarmı sessizce silerdi.
		slog.Info("derin reorg alarmı işlendi; düğüm bayraksız yeniden başlatılmalı")
		return
	}

	// 3. P2P sunucusu ve eşler
	p2p := network.NewP2PServer(*port, bc)
	// Havuz kaydı ağ ve API işlem almadan önce yüklenir; her işlem kabul yolundan yeniden geçer.
	// Yüklenenler ilk senkronizasyondan sonra eşlere yeniden yayılır (network/restorerelay.go).
	if restored, skipped, err := p2p.LoadMempool(); err != nil {
		slog.Error("havuz kaydı yüklenemedi; boş havuzla devam ediliyor", "err", err)
	} else if restored+skipped > 0 {
		slog.Info("havuz kaydı yüklendi", "geri_yuklenen", restored, "atlanan", skipped)
	}
	for _, p := range strings.Split(*peerAddr, ",") {
		if p = strings.TrimSpace(p); p != "" {
			p2p.AddPeer(p)
			slog.Info("eş eklendi", "peer", p)
		}
	}

	// 4. Borsa API sunucusu
	apiServer := network.NewAPIServer(*apiPort, bc)
	apiServer.TLSCert, apiServer.TLSKey = *tlsCert, *tlsKey
	apiServer.Mempool = p2p.Mempool
	apiServer.OnTxAccepted = p2p.RelayTx

	errCh := make(chan error, 2)
	go func() { errCh <- fmt.Errorf("p2p: %w", p2p.Start()) }()
	go func() { errCh <- fmt.Errorf("api: %w", apiServer.Start()) }()

	// 5. Kazım (isteğe bağlı): zincir kilidi tutulmadan, uç değişince şablon yenilenerek.
	stopMining := make(chan struct{})
	if *mineTo != "" {
		miner, err := network.NewMiner(p2p, *mineTo)
		if err != nil {
			slog.Error("geçersiz kazım adresi", "address", *mineTo)
			os.Exit(2)
		}
		slog.Info("kazım başlıyor", "address", *mineTo, "network", params.Name, "is_parcacigi", threads)
		go miner.Run(stopMining)
	}

	// Havuz düzenli olarak (değiştiyse) ve kapanışta kaydedilir: çökmede en çok bir aralık kaybolur.
	stopSaver := make(chan struct{})
	saverDone := make(chan struct{})
	go func() {
		defer close(saverDone)
		t := time.NewTicker(mempoolSaveInterval)
		defer t.Stop()
		for {
			select {
			case <-stopSaver:
				return
			case <-t.C:
				if _, err := p2p.Mempool.Save(); err != nil {
					slog.Error("havuz kaydedilemedi", "err", err)
				}
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		slog.Error("sunucu durdu", "err", err)
	case s := <-sig:
		slog.Info("kapatma sinyali alındı", "signal", s.String())
	}

	close(stopMining)
	close(stopSaver)
	<-saverDone
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := apiServer.Shutdown(ctx); err != nil {
		slog.Error("API düzgün kapatılamadı", "err", err)
	}
	// Son kayıt API kapandıktan sonra, zincir kapanmadan (ertelenmiş bc.Close'dan önce) yapılır.
	if saved, err := p2p.Mempool.Save(); err != nil {
		slog.Error("havuz kapanışta kaydedilemedi", "err", err)
	} else if saved {
		slog.Info("havuz kaydedildi")
	}
}

// backendClose os.Exit'ten önce (ertelenmiş çağrılar çalışmaz) GPU kaynaklarını bırakır.
func backendClose(b gpu.Backend) {
	if b != nil {
		b.Close()
	}
}
