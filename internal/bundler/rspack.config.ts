export default {
    target: ["node", "es2020"],
    mode: "production",
    entry: "./index.ts",
    devtool: "source-map",
    optimization: {
        avoidEntryIife: true,
        minimize: false
    },
    output: {
        scriptType: "module",
        chunkFormat: "commonjs",
        iife: false
    },
    module: {
        rules: [
            {
                test: /\.ts$/,
                exclude: [/node_modules/],
                loader: "builtin:swc-loader",
                options: {
                    jsc: {
                        target: "es2020",
                        parser: {
                            syntax: "typescript",
                            jsx: false,
                            dynamicImport: false,
                            privateMethod: false,
                            functionBind: false,
                            exportDefaultFrom: false,
                            exportNamespaceFrom: false,
                            decorators: false,
                            decoratorsBeforeExport: false,
                            topLevelAwait: false,
                            importMeta: false
                        },
                    }
                },
                type: "javascript/auto",
            },
        ],
    },
    resolve: {
        extensions: [".ts"]
    }
};

