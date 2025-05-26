import { DataArray, pipeline, Tensor } from '@xenova/transformers';

// try download the model
pipeline('feature-extraction', 'Xenova/all-MiniLM-L6-v2', {
    quantized: true,
    revision: 'main',
}).then(extractor => {
    console.log('Model loaded');
}).catch(err => {
    process.exit(1);
});

let extractorPromise = pipeline('feature-extraction', 'Xenova/all-MiniLM-L6-v2', {
    quantized: true,
    revision: 'main',
});

export async function generateEmbedding(text: string | string[]): Promise<Tensor> {
  const extractor = await extractorPromise;

  const output = await extractor(text, { 
    pooling: 'mean', 
    normalize: true,
  });
  return output;
}