package indexer

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/symbols"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/shared/running"
)

var total_embedding int

type InitResult struct {
	Success bool
	Error   error
	Message string
}

type EmbeddingEngine struct {
	stop        chan struct{}
	initialized chan InitResult
}

func NewEmbeddingEngine() *EmbeddingEngine {
	p := &EmbeddingEngine{
		stop:        make(chan struct{}),
		initialized: make(chan InitResult, 1),
	}
	return p
}

func (p *EmbeddingEngine) Start(wg *sync.WaitGroup) {
	if !conf.Get().Symbols.EnvInstalled || !(conf.Get().Symbols.EnableFeature || conf.Get().Symbols.EnablePromptSearch) {
		log.Println("[EmbeddingEngine] EmbeddingEngine did not start")
		p.initialized <- InitResult{
			Success: false,
			Error:   nil,
			Message: "Environment not installed or feature disabled",
		}
		close(p.initialized)
		return
	}

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
			p.initialized <- InitResult{
				Success: false,
				Error:   err,
				Message: "Failed to start embedding child process",
			}
			close(p.initialized)
			return
		}

		log.Println("[EmbeddingEngine] EmbeddingEngine started successfully")
		p.initialized <- InitResult{
			Success: true,
			Error:   nil,
			Message: "EmbeddingEngine started successfully",
		}
		close(p.initialized)

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
	if !conf.Get().Symbols.EnvInstalled {
		return
	}

	for {
		allWorkspace := workspace.GetAll()
		workspaceIds := []int{}
		for _, ws := range allWorkspace {
			workspaceIds = append(workspaceIds, ws.Id)
		}
		if len(workspaceIds) == 0 {
			break
		}

		workspace2Functions := symbols.ScanPendingEmbeddingFunctionsWithWorkspaceIds(workspaceIds, 100)
		if len(workspace2Functions) == 0 {
			if total_embedding > 0 {
				total_embedding = 0
				symbols.EmbeddingBuildIndex()
			}
			break
		}
		// logTopN := 5
		// for _, functions := range workspace2Functions {
		// 	for _, fn := range functions {
		// 		log.Printf("[EmbeddingEngine] Embedding function: %s", fn)
		// 		logTopN--
		// 		if logTopN == 0 {
		// 			break
		// 		}
		// 	}
		// }

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

		err = symbols.UpdateEmbeddingFunctionsFlag(workspace2Functions)
		if err != nil {
			log.Printf("[EmbeddingEngine] Failed to update embedding functions flag: %v", err)
			break
		}

		total_embedding += count
		if total_embedding%10000 == 0 {
			log.Printf("[EmbeddingEngine] Embedded %d functions", total_embedding)
		}

		if total_embedding > 100000 {
			total_embedding = 0
			symbols.EmbeddingStop()
			time.Sleep(5 * time.Second)
			log.Printf("[EmbeddingEngine] Total embedding functions exceeded limit, restart...")
		}
	}
}

func (p *EmbeddingEngine) Stop() {
	if !conf.Get().Symbols.EnvInstalled || !conf.Get().Symbols.EnableFeature {
		return
	}

	close(p.stop)
	log.Printf("[EmbeddingEngine] EmbeddingEngine stopped")
	symbols.EmbeddingStop()
}

func (p *EmbeddingEngine) startEmbeddingProcess() error {
	nodePath := conf.Get().BinPath.Node
	if nodePath == "" {
		nodePath = filepath.Join(running.ExecutablePath(), "node/node")
	}

	serverJS := conf.Get().BinPath.EmbeddingServerJS
	if serverJS == "" {
		serverJS = filepath.Join(running.ExecutablePath(), "embedding/dist/index.js")
	}

	if nodePath == "" || serverJS == "" {
		return fmt.Errorf("node or embedding server js not found")
	}

	cmd := exec.Command(nodePath, serverJS, strconv.Itoa(conf.Get().Symbols.EmbeddingPort))

	// Connect standard input/output of the process to the parent process
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	log.Printf("[EmbeddingEngine] Started embedding process with PID: %d, port: %d", cmd.Process.Pid, conf.Get().Symbols.EmbeddingPort)
	go func() {
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
