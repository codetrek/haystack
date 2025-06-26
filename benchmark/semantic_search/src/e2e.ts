import { spawn } from 'child_process';
import path from 'path';
import fs from 'fs/promises';
import { fileURLToPath, pathToFileURL  } from 'url';
import { loadDatasetFromFile, DatasetItem } from "./index.js";
import { parseFileRange } from './parse_cpp.js';
import { getConfig } from './conf.js';

const __filename = fileURLToPath(import.meta.url);

export function launchExe(exePath: string, args: string[] = []) {
  const fullPath = path.resolve(exePath);
  console.log('launchExe:', fullPath, args);

  const child = spawn(fullPath, args, {
    detached: true,
    stdio: 'ignore',
  });

  child.unref();
  return child;
}

function sleep(ms: number) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function parseLog() {
  let result: {
    promptTokenCount: number;
    toolCallDetails?: { 
      toolCounts: any; 
      numRequests: number; 
      turnDuration: number; 
      availableToolCount: number; 
    };
  } = {
    promptTokenCount: 0,
    toolCallDetails: undefined
  }

  try {
    const log = await fs.readFile(getConfig().vscode_log, 'utf-8');
    const lines = log.split('\n');
    for (const line of lines) {
      const item = JSON.parse(line);
      if (item['eventname'] === 'toolCallDetails') {
        result['toolCallDetails'] = {
          'toolCounts': item['properties']['toolCounts'],
          'numRequests': item['measurements']['numRequests'],
          'turnDuration': item['measurements']['turnDuration'],
          'availableToolCount': item['measurements']['availableToolCount'],
        };
      } else if (item['eventname'] === 'panel.request') {
        result['promptTokenCount'] += item['measurements']['promptTokenCount'];
      }
    }
  } catch {
  }
  return result;
}

async function main() {
  const filePath = path.join(process.cwd(), 'dataset.json');
  const dataset = await loadDatasetFromFile(filePath);

  let tasks: string[] = [];
  let currentTask = '';
  let resultData = [];
  try {
    const fileData = await fs.readFile(path.join(process.cwd(), 'search_results.json'), 'utf-8');
    const items = JSON.parse(fileData);
    for (const item of items) {
      if (item.timecost) {
        resultData.push(item);
      }
    }
    await fs.writeFile(path.join(process.cwd(), 'search_results.json'), JSON.stringify(resultData, null, 2));

  } catch  {}


  for (const item of dataset) {
    if (item.summary && typeof item.summary === 'string') {
      const finish = resultData.find((result:any) => {
        return result.task.includes(item.summary);
      });

      if (finish) {
        console.log('finish:', item.summary);
        continue;
      }
      tasks.push(item.summary);
    }
  }

  let running = false;
  let taskStartTimestamp = 0;
  const id = setInterval(async () => {
    if (running) {
      return;
    }
    running = true;

    if (tasks.length == 0 && currentTask === '') {
        clearInterval(id);
        return;
    }

    let fireTask = false;
    let dumpToFile = false;
    if (currentTask === '') {
      currentTask = tasks[tasks.length - 1];
      tasks.pop();
      fireTask = true;
    }

    let resultData = [];
    
    try {
        const fileData = await fs.readFile(path.join(process.cwd(), 'search_results.json'), 'utf-8');
        resultData = JSON.parse(fileData);
    } catch {}

    if (performance.now() - taskStartTimestamp > 1000 * 60 * 25) {
        console.log('task timeout, restart current task:', currentTask);
        resultData.push({
            task: currentTask,
            timecost: performance.now() - taskStartTimestamp,
            result: 'timeout',
        });
        dumpToFile = true;
    }

    for (const item of resultData) {
      if (!item.task.includes(currentTask)) {
        continue;
      }

      const ghcLog = await parseLog();
      if (!ghcLog.toolCallDetails && item.result !== 'timeout') {
        break
      }

      if (item.result != 'timeout') {
        item['timecost'] = performance.now() - taskStartTimestamp;
        item['ghclog'] = ghcLog
      }
      dumpToFile = true;

      if (tasks.length > 0) {
        currentTask = tasks[tasks.length - 1];
        tasks.pop();
        fireTask = true;  
      } else {
        currentTask = '';
      }
      break;
    }

    if (dumpToFile) {
        await fs.writeFile(path.join(process.cwd(), 'search_results.json'), JSON.stringify(resultData, null, 2));
    }


    if (fireTask) {
        taskStartTimestamp = performance.now();
        // remove log file
        try {
            await fs.unlink(getConfig().vscode_log);
        } catch {
        }

        // try cancel the previous task
        launchExe(getConfig().a11y_exe, [
                'new', 
                'chromium-src-snapshot - Visual Studio Code', 
            ]);
        await sleep(5000);

        console.log('fireTask:', currentTask);
        launchExe(getConfig().a11y_exe, [
            'chat', 
            'chromium-src-snapshot - Visual Studio Code', 
            `Where is the code "${currentTask}", When you finish task, send the file path and line number back using \`set_task_result\` tool. `
        ]);
    }
    running = false;
  }, 1000);
}

async function checkAgentResult(resultPath: string, title: string) {
  // const fileData = await fs.readFile(path.join(process.cwd(), 'ghc.json'), 'utf-8');
  const fileData = await fs.readFile(resultPath, 'utf-8');
  const ghc = JSON.parse(fileData);

  
  const filePath = path.join(process.cwd(), 'dataset.json');
  const dataset = await loadDatasetFromFile(filePath);

  let matched = 0;
  let toolused: { [key: string]: number } = {};
  let matchedToolused: { [key: string]: number } = {};
  let timecost = 0;
  let matchedTimecost = 0;
  let timeoutCount = 0;
  let promptTokenCount = 0;
  let matchedPromptTokenCount = 0;
  for (const item of ghc) {
    if (item.result === 'timeout') {
      timeoutCount++;
      continue;
    }

    item.filePath = item.filePath.replace(getConfig().workspace, '').replace(/\//g, '\\');
    if (!item['ghclog']) {
      continue;
    }
    const tools = JSON.parse(item.ghclog.toolCallDetails.toolCounts);
    timecost += item.timecost;
    promptTokenCount += item.ghclog.promptTokenCount;
    for (const key in tools) {
      if (toolused[key]) {
        toolused[key] += tools[key];
      } else {
        toolused[key] = tools[key];
      }
    }
    
    for (const task of dataset) {
      if (item.task.includes(task.summary)) {
        task.filePath = task.filePath.replace(getConfig().workspace, '').replace(/\//g, '\\');
        const expected = parseFileRange(task.filePath).path;
        if (item.filePath === expected) {
          matched++;
          matchedTimecost += item.timecost;
          matchedPromptTokenCount += item.ghclog.promptTokenCount;

          for (const key in tools) {
            if (matchedToolused[key]) {
              matchedToolused[key] += tools[key];
            } else {
              matchedToolused[key] = tools[key];
            }
          }
          break;
        }
      }
    }
  }
  

  console.log('\n' + '='.repeat(85));
  console.log(`\x1b[1m\x1b[36m📊 ${title} 📊\x1b[0m`);
  console.log('='.repeat(85));
  
  const recallRate = (matched / ghc.length * 100).toFixed(2);
  const recallColor = parseFloat(recallRate) > 75 ? '\x1b[32m' : parseFloat(recallRate) > 50 ? '\x1b[33m' : '\x1b[31m';
  
  console.log(`\x1b[1m🎯 Matched:\x1b[0m ${matched} / ${ghc.length} | \x1b[1mRecall rate:\x1b[0m ${recallColor}${recallRate}%\x1b[0m  | Timeout: ${timeoutCount}`);
  
  console.log('\n\x1b[1m\x1b[36m🔧 TOOL USAGE DETAILS 🔧\x1b[0m');
  console.log('-'.repeat(85));
  
  const toolsTable = [];
  for (const key in toolused) {
    const matchedAvg = (matchedToolused[key] / matched).toFixed(2);
    const totalAvg = (toolused[key] / ghc.length).toFixed(2);
    
    toolsTable.push({
      tool: key,
      total: toolused[key],
      matchedAvg: matchedAvg,
      totalAvg: totalAvg
    });
  }
  
  if (toolsTable.length > 0) {
    console.log(`\x1b[1m${'Tool'.padEnd(40)} | ${'Total Calls'.padEnd(12)} | ${'Avg/Success'.padEnd(12)} | ${'Avg/Total'.padEnd(12)}\x1b[0m`);
    console.log('-'.repeat(85));
    
    toolsTable.forEach(row => {
      console.log(
        `${row.tool.padEnd(40)} | ${row.total.toString().padEnd(12)} | ${row.matchedAvg.padEnd(12)} | ${row.totalAvg.padEnd(12)}`
      );
    });
  } else {
    console.log('No tool usage data available.');
  }
  
  let matched_toolused: { [key: string]: number } = {};
  let total_toolused: { [key: string]: number } = {};
  for (const key in toolused) {
    matched_toolused['matched_avg_' + key] = parseFloat((toolused[key] / matched).toFixed(2));
    total_toolused['total_avg_' + key] = parseFloat((toolused[key] / ghc.length).toFixed(2));
  }
  
  const matchedTimeSeconds = (timecost / 1000 / matched).toFixed(2);
  const totalTimeSeconds = (timecost / 1000 / ghc.length).toFixed(2);
  
  console.log('\n\x1b[1m\x1b[36m⏱️ TIME PERFORMANCE ⏱️\x1b[0m');
  console.log('-'.repeat(85));
  console.log(`\x1b[1mAverage time (successful searches):\x1b[0m \x1b[32m${matchedTimeSeconds} seconds\x1b[0m`);
  console.log(`\x1b[1mAverage time (all searches):       \x1b[0m \x1b[33m${totalTimeSeconds} seconds\x1b[0m`);


  console.log('\n\x1b[1m\x1b[36m💰 TOKEN COST 💰\x1b[0m');
  console.log('-'.repeat(85));
  // console.log(`\x1b[1mAverage token cost:\x1b[0m \x1b[32m${ (37720058 / ghc.length / 1000).toFixed(2) } k\x1b[0m`);
  console.log(`\x1b[1mAverage token cost (successful searches):\x1b[0m \x1b[32m${ (matchedPromptTokenCount / matched / 1000).toFixed(2) } k\x1b[0m`);
  console.log(`\x1b[1mAverage token cost (all searches):       \x1b[0m \x1b[32m${ (promptTokenCount / ghc.length / 1000).toFixed(2) } k\x1b[0m`);

  console.log('='.repeat(85) + '\n');
}

async function report() {
  await checkAgentResult(path.join(process.cwd(), 'ghc.json'), "Bechmark result: GHC semantic search");
  await checkAgentResult(path.join(process.cwd(), 'ghc_hs.json'), "Bechmark result: GHC + Haystack");
  await checkAgentResult(path.join(process.cwd(), 'ghc_hs_o_bd.json'), "Bechmark result: GHC + Haystack - RemotelyIndex");
}

if (path.resolve(__filename) === path.resolve(process.argv[1])) {
  main().catch((error: unknown) => {
    console.error('Error in main execution:', error);
    process.exit(1);
  });

  // report().catch((error: unknown) => {
  //   console.error('Error in main execution:', error);
  //   process.exit(1);
  // });

}