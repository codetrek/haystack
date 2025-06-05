import * as lancedb from "@lancedb/lancedb";
import { Schema, Field, Float32, Int32, Utf8, FixedSizeList, List } from "apache-arrow";
import { generateEmbedding } from "./embedding.js";
import * as fs from "fs/promises"

const dbVersion = 1;
const vectorDimensions = 384;

export interface embeddingInfo {
  symbolName: string;
  vector: number[];
}

export interface queryResult {
  symbol: string;
  score: number;
}

/**
 * LanceDB represents a vector database connection with basic functionality
 */
export class LanceDB {
  private db: lancedb.Connection;
  private useDelta: boolean = false;
  
  public static async create(db_path: string): Promise<LanceDB> {
    const conn = await lancedb.connect(db_path);
    const db = new LanceDB(conn);
    await db.init();
    return db;
  }

  private constructor(db: lancedb.Connection) {
    this.db = db;
  }

  async tableExists(tableName: string): Promise<boolean> {
    if (!this.db) {
        return false;
    }

    try {
      await this.db.openTable(tableName);
      return true;
    } catch (err) {
      return false;
    }
  }

  /**
   * Initialize creates the table if it doesn't exist
   */
  private async init(): Promise<void> {
    let ver = await this.get('version');
    if (!ver) {
      await this.createLatestTable();
      await this.put('version', dbVersion.toString());
    }

    this.useDelta = await this.hasIndex("symbol_vec_delta");
  }

  private async get(key: string) : Promise<string | null> {
    if (!this.db) {
      return null;
    }

    try {
      const table = await this.db.openTable("meta");
      const result = await table.query().where(`key = ${key}`).select("value").toArray();
      if (result.length > 0) {
        return result[0].value;
      }
    } catch (err) {
      return null;
    }

    return null;
  }

  private async put(key: string, value: string) : Promise<void> {
    if (!this.db) {
      return;
    }

    try {
      const table = await this.db.openTable("meta");
      let existingValue = await this.get(key);
      if (existingValue) {
        await table.delete(`key = '${key}'`);
      }
      await table.add([{key, value}]);
    } catch (err) {
      return;
    }
  }

  private async createLatestTable(): Promise<void> {
    if (!await this.tableExists("meta")) {
      await this.db!.createTable("meta", [], { 
        schema: new Schema([
          new Field("key", new Utf8(), false),   // String label field
          new Field("value", new Utf8(), false),   // String label field
        ])
      });
    }

    if (!await this.tableExists("symbol_vec")) {
      const vectorType = new FixedSizeList(vectorDimensions, new Field('item', new Float32()));
      await this.db!.createTable("symbol_vec", [], { 
        schema: new Schema([
          new Field("symbol", new Utf8(), false),
          new Field("vector", vectorType, false),
        ])
      });

      let table = await this.db!.openTable("symbol_vec");
      await table.createIndex("symbol", {
        config: lancedb.Index.btree()
      });

      await this.db!.createTable("symbol_vec_delta", [], { 
        schema: new Schema([
          new Field("symbol", new Utf8(), false),
          new Field("vector", vectorType, false),
        ])
      });

      table = await this.db!.openTable("symbol_vec_delta");
      await table.createIndex("symbol", {
        config: lancedb.Index.btree()
      });
    }
  }

  /**
   * Get the table reference
   */
  async getTable(tableName: string): Promise<lancedb.Table> {
    return await this.db.openTable(tableName);
  }

  public async addVec(embeddings: embeddingInfo[]): Promise<void> {
    let data = [];
    for (const embedding of embeddings) {
      data.push({
        symbol: embedding.symbolName,
        vector: embedding.vector
      });
    }
    if (this.useDelta) {
      const table = await this.getTable("symbol_vec_delta");
      await table.add(data);
    } else {
      const table = await this.getTable("symbol_vec");
      await table.add(data);
    }
  }

  public async deleteVec(symbolNames: string[]): Promise<void> {
    const table = await this.getTable("symbol_vec");
    await table.delete(`symbol IN (${symbolNames.join(",")})`);

    if (this.useDelta) {
      const table = await this.getTable("symbol_vec_delta");
      await table.delete(`symbol IN (${symbolNames.join(",")})`);
    }
  }

  /**
   * Query vectors by similarity from both main and delta tables
   */
  public async querySymbols(query:string, limit: number = 10): Promise<queryResult[]> {
    let query_embedding = await generateEmbedding(query);
    const floatArray = new Float32Array(query_embedding.data);

    // Query the main table
    const mainTable = await this.getTable("symbol_vec");
    const mainResults = await mainTable.vectorSearch(floatArray).limit(limit).toArray();
    
    // Query the delta table if it exists
    let deltaResults: any[] = [];
    if (this.useDelta) {
      const deltaTable = await this.getTable("symbol_vec_delta");
      deltaResults = await deltaTable.vectorSearch(floatArray).limit(limit).toArray();
    }
    
    // Combine results from both tables
    const allResults = [
      ...mainResults.map(result => ({ 
        symbol: result.symbol, 
        score: result._distance,
        source: 'main'
      })),
      ...deltaResults.map(result => ({ 
        symbol: result.symbol, 
        score: result._distance,
        source: 'delta'
      }))
    ];
    
    // Sort by score (lower distance is better in vector search)
    allResults.sort((a, b) => a.score - b.score);
    
    // Return the top results after removing duplicates by symbol (keeping the best score)
    const seenIds = new Set<number>();
    const dedupedResults: queryResult[] = [];
    
    for (const result of allResults) {
      if (!seenIds.has(result.symbol)) {
        dedupedResults.push({
          symbol: result.symbol,
          score: result.score
        });
        seenIds.add(result.symbol);
        
        // Break if we have enough results
        if (dedupedResults.length >= limit) {
          break;
        }
      }
    }
    
    return dedupedResults;
  }

  /**
   * Create search index on vector column for faster searches
   */
  async createIndex(numPartitions: number = 2048, numSubVectors: number = 96): Promise<void> {
    const table = await this.getTable("symbol_vec");
    await table.createIndex("vector", {
      config: lancedb.Index.ivfPq({
        numPartitions: numPartitions,
        numSubVectors: numSubVectors,
      }),
    });
    this.useDelta = true;
  }

  public async hasIndex(table: string): Promise<boolean> {
    const tbl = await this.getTable(table);
    return (await tbl.listIndices()).some(index => index.name === "vector_idx");
  }

  async count(table: string): Promise<number> {
    const tbl = await this.getTable(table);
    return await tbl.countRows();
  }

  /**
   * Close the database connection
   */
  async close(): Promise<void> {
    await this.db.close();
  }
}

const workspace2VectorDB: Map<string, LanceDB> = new Map();

export interface embeddingResult {
  code: number;
  message: string;
}

export async function embeddingSymbolsToDB(dbPath: string, symbols: string[]): Promise<embeddingResult> {
  if (!workspace2VectorDB.has(dbPath)) {
    workspace2VectorDB.set(dbPath, await LanceDB.create(dbPath));
  }
  const lancedb = workspace2VectorDB.get(dbPath);

  let embeddings: embeddingInfo[] = [];

  try {
    let output = await generateEmbedding(symbols);

    const batchSuccess = output.dims[0] === symbols.length;
    
    if (batchSuccess) {
      const embeddingDim = output.dims[1];
      for (let j = 0; j < symbols.length; j++) {
        const startIdx = j * embeddingDim;
        const endIdx = startIdx + embeddingDim;
            
        // Extract single embedding
        const singleEmbedding = output.data.slice(startIdx, endIdx);
            
        // Convert to array and store
        const embeddingArray = Array.from(singleEmbedding);
        embeddings.push({
          symbolName: symbols[j],
          vector: embeddingArray
        } as embeddingInfo);
      }

      if (embeddings.length === 0) {
        return {
          code: 1,
          message: 'Empty embeddings for batch'
        };
      }
      lancedb?.addVec(embeddings);
    } else {
      return {
        code: 1,
        message: 'Failed to generate embeddings for batch'
      };
      
    }
  } catch (err) {
    return {
      code: 1,
      message: 'Exception while generating embeddings for batch: ' + err
    };
  }

  return {
    code: 0,
    message: ''
  };
}

export interface querySymbolsResult {
  code: number;
  message: string;
  data: queryResult[];
}

export async function querySymbols(dbPath: string, query: string, limit: number = 10): Promise<querySymbolsResult> {
  if (!workspace2VectorDB.has(dbPath)) {
    workspace2VectorDB.set(dbPath, await LanceDB.create(dbPath));
  }
  const lancedb = workspace2VectorDB.get(dbPath);
  const data = await lancedb?.querySymbols(query, limit);
  
  return {
    code: 0,
    message: '',
    data: data || []
  }
}

export async function buildIndexIfNeeded(): Promise<void> {
  for (const [dbPath, lancedb] of workspace2VectorDB) {
    const indexExists = await lancedb.hasIndex("symbol_vec");
    const rows = await lancedb.count("symbol_vec");
    console.log(`Index exists: ${indexExists}, rows: ${rows}, dbPath: ${dbPath}`);

    if (!indexExists && rows > 300000) {
      const now = performance.now();
      console.log(`Creating index for ${dbPath}`);
      await lancedb.createIndex();
      console.log(`Index created for ${dbPath}, rows: ${rows}, time: ${performance.now() - now}`);
    }
  }
  return;
}

export async function removeDB(dbPath: string): Promise<void> {
  if (workspace2VectorDB.has(dbPath)) {
    const lancedb = workspace2VectorDB.get(dbPath);
    await lancedb?.close();
    workspace2VectorDB.delete(dbPath);
  }
  await fs.rm(dbPath, { recursive: true });
}
