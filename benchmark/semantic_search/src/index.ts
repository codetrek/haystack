import { promises as fsPromises } from 'fs';
import path from 'path';
import { fileURLToPath, pathToFileURL  } from 'url';
import { dirname } from 'path';
import { parseCppFunctions, parseFileRange, getRandomCcFiles} from './parse_cpp.js';
import { summarizeFunctionBody } from './summarize_function.js';
import { matchFromHaystack } from './query.js';
import { getConfig, loadConfigFromFile, updateConfig, saveConfigToFile } from './conf.js';

/**
 * Interface representing a dataset item with function information
 */
export interface DatasetItem {
  filePath: string;
  function: string;
  codesnippet: string;
  filepos: number;
  pos: number;
  summary?: string;
  timecost: number;
  filemrr: number;
  ghc_query: string;
}

interface SSResult {
  summary: string;
  query: string;
  files: Array<{file: string, line: number}>;
}

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

async function generateChartHtml(database: any) {
  try {
    const templatePath = path.join(path.dirname(__filename), 'template.html');
    let templateContent = await fsPromises.readFile(templatePath, 'utf8');
    templateContent = templateContent.replace('{{dataset}}', JSON.stringify(database));

    const outputPath = path.join(process.cwd(), 'report.html');
    await fsPromises.writeFile(outputPath, templateContent);
    console.log('Successfully generated report.html');  
  } catch (error) {
    console.error('Error generating chart HTML:', error);
  }
}

function generateDataset(count: number = 50) : DatasetItem[] {
  // Define the directories to search for C++ files
  // const directories = [ getConfig().workspace ];
  const randomFiles = getRandomCcFiles(getConfig().subdirectories, 9999);
  
  const longFunctions = [];
  console.log(`Found ${randomFiles.length} C++ files`);
  
  const minLineCount = 20;
  for (const file of randomFiles) {
    try {
      const functions = parseCppFunctions(file);
      const longFunctionsInFile = functions.filter((func: any) => {
        // const lineCount = func.endLine - func.startLine + 1;
        const lineCount = func.body.split('\n').filter((line: string) => line.trim() !== '').length;
        return lineCount > minLineCount;
      });
      
      longFunctions.push(...longFunctionsInFile);    } catch (error: unknown) {
      console.error(`Error processing file ${file}:`, error);
    }
  }
  
  console.log(`Found ${longFunctions.length} functions with more than ${minLineCount} lines`);
  
  if (longFunctions.length <= count) {
    console.warn(`Warning: Only found ${longFunctions.length} functions with more than 100 lines`);
    return longFunctions.map(func => ({
      filePath: `${func.file}:${func.startLine}-${func.endLine}`,
      function: `${func.name}`,
      codesnippet: func.body,
      filepos: -1,
      pos: 1,
      timecost: 0,
      filemrr: 1,
      ghc_query: ''
    }));
  }
  
  const selectedFunctions = [];
  const availableFunctions = [...longFunctions];
  
  for (let i = 0; i < count && availableFunctions.length > 0; i++) {
    const randomIndex = Math.floor(Math.random() * availableFunctions.length);
    selectedFunctions.push(availableFunctions[randomIndex]);
    availableFunctions.splice(randomIndex, 1);
  }

  // Transform the functions into the dataset format
  return selectedFunctions.map((func, index) => ({
    filePath: `${func.file}:${func.startLine}-${func.endLine}`,
    function: `${func.name}`,
    codesnippet: func.body,
    filepos: -1,
    pos: -1,
    timecost: 0,
    filemrr: 1,
    ghc_query: ''
  }));
}

async function summaryFunction(dataset: DatasetItem[]){
  console.log('Generating summaries for functions...');
  for (const item of dataset) {
    try {
      const summary = await summarizeFunctionBody(item.codesnippet);
      item.summary = summary;
      console.log(`Generated summary for "${item.function}": ${summary}`);    
    } catch (error) {
      console.error(`Error generating summary for "${item.function}":`, error);
      item.summary = item.function;
    }
  }
  
  console.log('Finished generating summaries for all functions');
  return dataset;
}

async function saveDatasetToFile(dataset: DatasetItem[], filePath: string = 'dataset.json'): Promise<void>{
  try {
    if (!path.isAbsolute(filePath)) {
      filePath = path.join(__dirname, filePath);
    }
    
    // Convert the dataset to JSON string with formatting for readability
    const jsonData = JSON.stringify(dataset, null, 2);
    
    // Write to file
    await fsPromises.writeFile(filePath, jsonData, 'utf8');
    console.log(`Dataset successfully saved to ${filePath}`);  
  } catch (error) {
    console.error('Error saving dataset to file:', error);
    throw error;
  }
}

export async function loadDatasetFromFile(filePath: string = 'dataset.json'): Promise<DatasetItem[]>{
  try {
    if (!path.isAbsolute(filePath)) {
      filePath = path.join(__dirname, filePath);
    }
    const jsonData = await fsPromises.readFile(filePath, 'utf8');
    let dataset = JSON.parse(jsonData);
    for (let item of dataset) {
      item.summary = item.summary.replace(/\.$/, '');
    }
    return dataset;  
  } catch (error) {
    console.error('Error loading dataset from file:', error);
    throw error;
  }
}

async function matchFromHaystackForDataset(dataset: DatasetItem[]): Promise<DatasetItem[]> {
  for (const item of dataset) {
    try {
      if (!item.summary) {
        continue;
      }
      const f = parseFileRange(item.filePath);
      const start = performance.now();
      const match = await matchFromHaystack(item.summary, 200, 500, getConfig().workspace, f.path, f.startLine);
      const end = performance.now();
      item.filepos = match[0];
      item.pos = match[1];
      item.timecost = end - start;
      if (match[0] > 0 && match[2] > 0) {
        // item.filemrr = Math.round(match[0] / match[2] * 10) / 10;
        item.filemrr = 1 / match[0];
      } else {
        item.filemrr = 0;
      }
      console.log(`Matched function "${item.function}" to Haystack index ${match}`, item.filemrr);
    } catch (error) {
      console.error(`Error matching function "${item.summary}" to Haystack:`, error);
      process.exit(1);
    }
  }

  return dataset;
}

async function loadGHCResult() : Promise<Array<SSResult>> {
  const filePath = path.join(process.cwd(), '../search_results.json')
  
  const jsonData = await fsPromises.readFile(filePath, 'utf8');
  const res = JSON.parse(jsonData);
  
  const parsedRes: Array<SSResult> = [];
  for (const item of res) {
    if (!item.summary) {
      continue;
    }

    const files = JSON.parse(item.result.replace(/\\n/g, '\n').replace(/\\"/g, '"'));
    const r: SSResult = {
      summary: item.summary,
      query: item.words,
      files: []
    }

    // 
    for (let file of files) {
      if (file.startsWith("/")) {
        file = file.slice(1);
      }
      const [path, lineStr] = file.split(":");
      r.files.push({
        file: path,
        line: parseInt(lineStr)
      });
    }
    parsedRes.push(r);
  }
  return parsedRes
}

async function main() {
  try {
    await loadConfigFromFile(path.join(process.cwd(), 'config.json'));
  } catch (error) {
    console.error('Error default loading configuration:', error); 
  }

  try {
    await loadConfigFromFile(path.join(process.cwd(), 'config.local.json'));
  } catch (error) {
    console.error('Error local loading configuration:', error); 
  }
  
  const args = process.argv.slice(2);
  let dataset: DatasetItem[];
   
  if (args.length > 0 && args[0] === '--load') {
    const filePath = args[1] || path.join(process.cwd(), 'dataset.json');
    dataset = await loadDatasetFromFile(filePath);
    const rr = await loadGHCResult();
    for (let i = 0; i < dataset.length; i++) {
      // search for the summary
      for (let j = 0; j < rr.length; j++) {
        if (dataset[i].summary === rr[j].summary) {
          dataset[i].ghc_query = rr[j].query;
          break;
        }
      }
    }
    
    for (let i = 0; i < dataset.length; i++) {
      dataset[i].filemrr = 0;
      if (dataset[i].ghc_query === '') {
        continue;
      }

      const ghcMatchedFiles = rr.find((item) => item.query === dataset[i].ghc_query);
      if (ghcMatchedFiles) {
        let idx = 1;
        for (const file of ghcMatchedFiles.files) {
          const searchedFile = (getConfig().workspace + '/' + file.file).replace(/\\/g, '/');
          const f = parseFileRange(dataset[i].filePath);
          const dstFile = f.path.replace(/\\/g, '/');

          if (searchedFile === dstFile) {
            console.log(`searchedFile: ${searchedFile}, dstFile: ${dstFile}`);  
            dataset[i].filemrr = 1 / idx;
            break;
          }
          idx++;
        }
      }
      console.log(`Matched function "${dataset[i].function}"`, dataset[i].filemrr);
    }



  } else {
    dataset = generateDataset(300);
    dataset = await summaryFunction(dataset);
    await saveDatasetToFile(dataset, path.join(process.cwd(), 'dataset.json'));
  }

  // dataset = await matchFromHaystackForDataset(dataset);
  await generateChartHtml(dataset);

  // let sumRank = 0.0
  // for (const item of dataset) {
  //   sumRank += item.filemrr;
  // }
  // console.log(`Average MRR: ${sumRank / dataset.length}`);

  console.log('Done!');
}


if (path.resolve(__filename) === path.resolve(process.argv[1])) {
  main().catch((error: unknown) => {
    console.error('Error in main execution:', error);
    process.exit(1);
  });
}