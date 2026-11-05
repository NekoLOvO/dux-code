pragma Singleton

import QtQuick
import Quickshell

// Scoring for the settings search.
//
// The whole point of this file is to be small: the index is built from widget titles that already
// exist, so what is left is "given what the user typed, how well does this row match". Everything is
// lowercased word-prefix matching — no fuzzy distance, no ranking table to maintain.
Singleton {
    function words(text) {
        return String(text ?? "").toLowerCase().split(/\s+/).filter(word => word !== "");
    }

    // Rank of one row against a query. 0 means "no match"; higher is better.
    //
    // Every word of the query has to land somewhere, so "font scale" only matches a row about fonts
    // that also mentions scale, rather than everything containing either word.
    function score(query, title, subtitle, keywords) {
        const queryWords = words(query);
        if (queryWords.length === 0)
            return 0;

        const titleText = String(title ?? "").toLowerCase();
        const titleWords = words(titleText);
        const subtitleText = String(subtitle ?? "").toLowerCase();
        const keywordWords = words(keywords);

        let total = 0;
        for (const word of queryWords) {
            let best = 0;
            if (titleText === word)
                best = 5;                       // the row is exactly this
            else if (titleText.startsWith(word))
                best = 4;                       // and starts with it
            else if (titleWords.some(part => part.startsWith(word)))
                best = 3;                       // a word of the title starts with it
            else if (keywordWords.some(part => part.startsWith(word)))
                best = 2;                       // a hand-written synonym
            else if (titleText.includes(word) || subtitleText.includes(word) || keywordWords.some(part => part.includes(word)))
                best = 1;                       // mentioned somewhere
            if (best === 0)
                return 0;
            total += best;
        }
        return total;
    }
}
