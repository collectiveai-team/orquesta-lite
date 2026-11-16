import {definePlugin, runWorker} from "@paperclipai/plugin-sdk";
import {installRelay} from "./relay.mjs";

const plugin = definePlugin({async setup(ctx) { installRelay(ctx); }});
export default plugin;
runWorker(plugin, import.meta.url);
