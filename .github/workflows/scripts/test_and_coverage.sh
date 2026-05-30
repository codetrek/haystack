#!/bin/bash
# Run coverage tool in CI mode
# This script is called by GitHub Actions workflow

set -e

EXCLUDE_FUNCS="PutNode,DeleteNode,SetNodeMapping,DeleteNodeMapping,writeMetaHeader,writeDataFileHeader,initAllFiles,mmapAll,Replay,setNeighborsUpper,remapFile,Close,OpenWAL,growFile,ensureUpperCapacity,OpenMmapStore,syncAll,closeMmaps,compactIdmap,replayWAL,init,Sync,InsertBatch" \
  go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0
