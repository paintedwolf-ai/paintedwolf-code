/**
 * IIFE entry for go:embed / CDP injection. Builds window.__lycaonDriver.
 */
import { installLycaonDriver } from "./page-api.ts";

installLycaonDriver();
