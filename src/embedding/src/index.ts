import http from 'http';
import { embeddingSymbolsToDB, querySymbols} from './lanceDB.js';
import { generateEmbedding } from './embedding.js';

/**
 * requestBody {
 *   dbPath: string;
 *   functions: string[]
 * }
 */
function handleEmbeddingSymbolToDB(req: http.IncomingMessage, res: http.ServerResponse) {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'application/json');
  let data = '';
  req.on('data', chunk => {
    data += chunk;
  });
  
  req.on('end', async () => {
    try {
      const requestBody = JSON.parse(data);
      if (!requestBody.dbPath || !requestBody.functions) {
        res.end(JSON.stringify({ 
          code: 1, 
          message: 'Invalid request body' 
        }));
        return;
      }

      const resp = await embeddingSymbolsToDB(requestBody.dbPath, requestBody.functions);
      res.end(JSON.stringify({ resp}));
    } catch (error) {
      res.end(JSON.stringify({ 
        code: 1, 
        message: 'Bad request' 
      }));
    }
  });
}

/**
 * requestBody {
 *   text: string;
 * }
 * 
 * resp {
 *  code: 0,
 *  embedding: Float32Array 
 * }
 */
function handleEmbedding(req: http.IncomingMessage, res: http.ServerResponse) {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'application/json');
  let data = '';
  req.on('data', chunk => {
    data += chunk;
  });
  
  req.on('end', async () => {
    try {
      const requestBody = JSON.parse(data);
      if (!requestBody.text) {
        res.end(JSON.stringify({ 
          code: 1, 
          message: 'Invalid request body' 
        }));
        return;
      }

      let query_embedding = await generateEmbedding(requestBody.text);
      
      res.end(JSON.stringify({ 
        code: 0, 
        embedding: new Float32Array(query_embedding.data)
      }));
    } catch (error) {
      res.end(JSON.stringify({ 
        code: 1, 
        message: 'Bad request' 
      }));
    }
  });
}

/**
 * 
 * requestBody {
 *   query: string;
 *   dbPath: string;
 *   limit: number;
 * }
 */
function handleQuery(req: http.IncomingMessage, res: http.ServerResponse) {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'application/json');
  let data = '';
  req.on('data', chunk => {
    data += chunk;
  });
  
  req.on('end', async () => {
    try {
      const requestBody = JSON.parse(data);
      if (!requestBody.query || !requestBody.dbPath || !requestBody.limit) {
        res.end(JSON.stringify({ 
          code: 1, 
          message: 'Invalid request body' 
        }));
        return;
      }

      const resp = await querySymbols(requestBody.dbPath, requestBody.query, requestBody.limit);
      res.end(JSON.stringify(resp));
    } catch (error) {
      res.end(JSON.stringify({ 
        code: 1, 
        message: 'Bad request',
        data: []
      }));
    }
  });
}

const server = http.createServer((req, res) => {
  console.log(`Received request: ${req.method} ${req.url}`);

  if (req.url === '/health') {
    res.statusCode = 200;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ code: 0 }));
    return;
  }

  if (req.url === '/stop' && req.method === 'POST') {
    res.statusCode = 200;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ code: 0 }));
    console.log('Stop API called. Exiting process...');
    process.exit(0);
    return;
  }

  if (req.url === '/embeddingSymbolToDB' && req.method === 'POST') {
    handleEmbeddingSymbolToDB(req, res);
    return;
  }

  if (req.url === '/query' && req.method === 'POST') {
    handleQuery(req, res);
    return;
  }


  if (req.url === '/embedding' && req.method === 'POST') {
    handleEmbedding(req, res);
    return;
  }
  
  res.statusCode = 404;
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({ code: 1, message: 'Not Found' }));
});

// Get port from command line arguments
if (!process.argv[2]) {
  console.error('Error: Port number is required. Please provide a port number as a command line argument.');
  process.exit(1);
}

const PORT = parseInt(process.argv[2]);
if (isNaN(PORT)) {
  console.error('Error: Invalid port number. Please provide a valid number.');
  process.exit(1);
}

server.listen(PORT, () => {
  console.log(`Server is running at http://localhost:${PORT}/`);
});
