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
interface DatasetItem {
  filePath: string;
  function: string;
  codesnippet: string;
  filepos: number;
  pos: number;
  query?: string;
  timecost: number;
  filemrr: number;
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
  const directories = [ getConfig().workspace ];
  const randomFiles = getRandomCcFiles(directories, 9999);
  
  const longFunctions = [];
  console.log(`Found ${randomFiles.length} C++ files`);
  
  const minLineCount = 30;
  for (const file of randomFiles) {
    try {
      const functions = parseCppFunctions(file);
      const longFunctionsInFile = functions.filter((func: any) => {
        const lineCount = func.endLine - func.startLine + 1;
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
      filemrr: 1
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
    filemrr: 1
  }));
}

async function summaryFunction(dataset: DatasetItem[]){
  console.log('Generating summaries for functions...');
  for (const item of dataset) {
    try {
      const summary = await summarizeFunctionBody(item.codesnippet);
      item.query = summary;
      console.log(`Generated summary for "${item.function}": ${summary}`);    
    } catch (error) {
      console.error(`Error generating summary for "${item.function}":`, error);
      item.query = item.function;
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

async function loadDatasetFromFile(filePath: string = 'dataset.json'): Promise<DatasetItem[]>{
  try {
    if (!path.isAbsolute(filePath)) {
      filePath = path.join(__dirname, filePath);
    }
    
    const jsonData = await fsPromises.readFile(filePath, 'utf8');
    
    const dataset = JSON.parse(jsonData);
    console.log(`Dataset successfully loaded from ${filePath}`);
    
    return dataset;  
  } catch (error) {
    console.error('Error loading dataset from file:', error);
    throw error;
  }
}

async function matchFromHaystackForDataset(dataset: DatasetItem[]): Promise<DatasetItem[]> {
  for (const item of dataset) {
    try {
      if (!item.query) {
        continue;
      }
      const f = parseFileRange(item.filePath);
      const start = performance.now();
      const match = await matchFromHaystack(item.query, 300, 500, getConfig().workspace, f.path, f.startLine);
      const end = performance.now();
      item.filepos = match[0];
      item.pos = match[1];
      item.timecost = end - start;
      if (match[0] > 0 && match[2] > 0) {
        item.filemrr = Math.round(match[0] / match[2] * 10) / 10;
      } else {
        item.filemrr = 1;
      }
      console.log(`Matched function "${item.function}" to Haystack index ${match}`, item.filemrr);
    } catch (error) {
      console.error(`Error matching function "${item.query}" to Haystack:`, error);
      process.exit(1);
    }
  }

  return dataset;
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
  } else {
    dataset = generateDataset(200);
    dataset = await summaryFunction(dataset);
    await saveDatasetToFile(dataset, path.join(process.cwd(), 'dataset.json'));
  }

  dataset = await matchFromHaystackForDataset(dataset);
  await generateChartHtml(dataset);
  console.log('Done!');
}

main().catch((error: unknown) => {
  console.error('Error in main execution:', error);
  process.exit(1);
});
