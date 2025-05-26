package indexer

import (
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/symbols"
	"github.com/ai-microsoft/haystack/shared/running"
)

var total_embedding int

type EmbeddingEngine struct {
	stop chan struct{}
}

func NewEmbeddingEngine() *EmbeddingEngine {
	p := &EmbeddingEngine{
		stop: make(chan struct{}),
	}
	return p
}

func (p *EmbeddingEngine) Start(wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		st, err := symbols.EmbeddingHealth()
		if err == nil && st.Code == 0 {
			log.Println("[EmbeddingEngine] EmbeddingEngine is already running, kill...")
			symbols.EmbeddingStop()
		}

		if err := p.startEmbeddingProcess(); err != nil {
			log.Printf("[EmbeddingEngine] Failed to start embedding child process: %v", err)
			return
		}

		for {
			select {
			case <-p.stop:
				return
			case <-time.After(10 * time.Second):
				p.processPendingEmbeddings()
			}
		}
	}()
}

func (p *EmbeddingEngine) processPendingEmbeddings() {
	if !conf.Get().Embedding.EnvInstalled || !conf.Get().Embedding.EmbeddingSymbols {
		return
	}

	for {
		workspace2Functions := symbols.ScanPendingEmbeddingFunctions(500)
		if len(workspace2Functions) == 0 {
			break
		}

		workspace2Functions, err := symbols.RemoveComputedEmbeddings(workspace2Functions)
		if err != nil {
			log.Printf("[EmbeddingEngine] Failed to remove computed embeddings: %v", err)
			break
		}
		if len(workspace2Functions) == 0 {
			continue
		}

		count, err := symbols.EmbeddingAddSymbolsToDB(workspace2Functions)
		if err != nil {
			break
		}
		log.Printf("[EmbeddingEngine] Embedded %d functions", count)

		err = symbols.UpdateEmbeddingFunctionsFlag(workspace2Functions)
		if err != nil {
			log.Printf("[EmbeddingEngine] Failed to update embedding functions flag: %v", err)
			break
		}

		total_embedding += count

		if total_embedding > 50000 {
			total_embedding = 0
			symbols.EmbeddingStop()
			time.Sleep(5 * time.Second)
			log.Printf("[EmbeddingEngine] Total embedding functions exceeded limit, restart...")
		}
	}
}

func (p *EmbeddingEngine) Stop() {
	close(p.stop)
	log.Printf("[EmbeddingEngine] EmbeddingEngine stopped")
	symbols.EmbeddingStop()
}

func (p *EmbeddingEngine) startEmbeddingProcess() error {
	nodePath := filepath.Join(running.ExecutablePath(), "node/node")
	serverJs := filepath.Join(running.ExecutablePath(), "embedding/dist/index.js")

	cmd := exec.Command(nodePath, serverJs, strconv.Itoa(conf.Get().Embedding.Port))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	log.Printf("[EmbeddingEngine] Started embedding process with PID: %d, port: %d", cmd.Process.Pid, conf.Get().Embedding.Port)

	go func() {
		defer stdin.Close()
		defer stdout.Close()
		err := cmd.Wait()
		if err != nil {
			log.Printf("[EmbeddingEngine] Child process exited with error: %v, attempting restart...", err)
		} else {
			log.Println("[EmbeddingEngine] Child process exited normally, attempting restart...")
		}

		// Check if we should restart
		select {
		case <-p.stop:
			log.Println("[EmbeddingEngine] EmbeddingEngine is stopping, not restarting child process")
			return
		default:
			time.Sleep(time.Second)
			if err := p.startEmbeddingProcess(); err != nil {
				log.Printf("[EmbeddingEngine] Failed to restart child process: %v", err)
			}
		}
	}()

	return nil
}
