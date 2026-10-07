import { createExercise } from './exercise.js';
const test = createExercise('spike');
export const options = test.options;
export const setup = test.setup;
export const operation = test.operation;
export const handleSummary = test.handleSummary;
