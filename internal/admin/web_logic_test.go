//go:build uitest

// Behaviour of the SPA's pure helpers — the code that turns stored values into
// what the flow editor shows and back. A mistake there changes routing without
// a sound (a condition saved as something else), so the block between the
// "pure" markers is executed under node with the checks below, not just parsed.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run Logic
package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var pureRe = regexp.MustCompile(`(?s)// ---- pure: begin ----(.*?)// ---- pure: end ----`)

func TestLogicHelpers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the helper checks")
	}
	m := pureRe.FindStringSubmatch(string(indexHTML))
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	f := filepath.Join(t.TempDir(), "logic.js")
	if err := os.WriteFile(f, []byte(m[1]+logicChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("helper checks failed:\n%s", out)
	}
}

const logicChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};

// A typed or pasted list.
eq(splitList('ru, us\nde|ua'), ['ru','us','de','ua'], 'commas, new lines and pipes separate');
eq(splitList(' a ,, b , A '), ['a','b'], 'trimmed, empties and repeats (any case) dropped');
eq(splitList(''), [], 'nothing typed');

// Which expressions read as words.
eq(altWords('/amazon|google|hetzner/i'), {words:['amazon','google','hetzner'],flags:'i'}, 'alternation with a flag');
eq(altWords('/buy|kupit/'), {words:['buy','kupit'],flags:''}, 'case-sensitive alternation');
eq(altWords('/example\\.com|foo\\.org/'), {words:['example.com','foo.org'],flags:''}, 'escaped dots are plain dots');
eq(altWords('/a\\|b|c/'), {words:['a|b','c'],flags:''}, 'an escaped pipe stays inside its word');
eq(altWords('/one word|two words/i'), {words:['one word','two words'],flags:'i'}, 'spaces are part of a word');
eq(altWords('/single/'), {words:['single'],flags:''}, 'one word');
['/^iphone/i', '/a{2,3}/', '/\\d+/', '/(a|b)/', '/iphone|/', '/|x/', '/a||b/', '//', 'amazon',
 '/x.y/', '/[ab]/', '/a+/', '/a$/', '/\\bword/'].forEach(s => eq(altWords(s), null, 'a real expression: ' + s));

// Words back to an expression.
eq(altRegex(['amazon','google'],'i'), '/amazon|google/i', 'words joined');
eq(altRegex(['example.com','a|b','c+d','(x)'],''), '/example\\.com|a\\|b|c\\+d|\\(x\\)/', 'special characters escaped');
[[['amazon','google'],'i'], [['example.com','foo.org'],''], [['a|b','c+d','[x]','^y$','1.5*2?'],'i']]
  .forEach(([w, f]) => eq(altWords(altRegex(w, f)), {words:w, flags:f}, 'round trip ' + w.join(' ')));

// How a stored condition is shown.
eq(listForm({flag:2,values:['ru','us'],raw:'ru,us'},false), {form:'words',words:['ru','us'],flags:'',raw:''}, 'values');
eq(listForm({flag:2,values:[],raw:'ru|ua'},false), {form:'words',words:['ru','ua'],flags:'',raw:''}, 'an old raw list with pipes');
eq(listForm({flag:2,values:['/amazon|google/i'],raw:'/amazon|google/i'},true),
  {form:'alt',words:['amazon','google'],flags:'i',raw:'/amazon|google/i'}, 'an alternation shows as words');
eq(listForm({flag:2,values:['/^Mozilla/'],raw:'/^Mozilla/'},true), {form:'regex',words:[],flags:'',raw:'/^Mozilla/'}, 'a real expression');
eq(listForm({flag:2,values:['/x/'],raw:''},true), {form:'words',words:['/x/'],flags:'',raw:''}, 'values alone are never an expression: the router reads raw');
eq(listForm(undefined,true), {form:'words',words:[],flags:'',raw:''}, 'no filter');

// What is stored.
const OFF = {flag:0,values:[],raw:''};
eq(listValue(2,'words',['ru','us']), {flag:2,values:['ru','us'],raw:'ru,us'}, 'words stored as values');
eq(listValue(2,'words',[]), OFF, 'an empty list is no filter');
eq(listValue(1,'words',['  ']), OFF, 'blank words dropped');
eq(listValue(2,'alt',['amazon','google'],'','/amazon|google/i','i'), {flag:2,values:['/amazon|google/i'],raw:'/amazon|google/i'}, 'untouched alternation kept');
eq(listValue(2,'alt',['a-b','c'],'','/a\\-b|c/',''), {flag:2,values:['/a\\-b|c/'],raw:'/a\\-b|c/'}, 'untouched alternation kept byte for byte');
eq(listValue(2,'alt',['amazon','google','ovh'],'','/amazon|google/i','i'), {flag:2,values:['/amazon|google|ovh/i'],raw:'/amazon|google|ovh/i'}, 'edited alternation keeps its flags');
eq(listValue(1,'alt',['buy','kupit','Pokupka'],'','/buy|kupit/',''), {flag:1,values:['/buy|kupit|Pokupka/'],raw:'/buy|kupit|Pokupka/'}, 'case-sensitive stays case-sensitive');
eq(listValue(2,'alt',[],'','/a|b/i','i'), OFF, 'every word removed');
eq(listValue(2,'regex',[],'/^Mozilla/i'), {flag:2,values:['/^Mozilla/i'],raw:'/^Mozilla/i'}, 'an expression');
eq(listValue(2,'regex',[],'abc'), {flag:2,values:['/abc/'],raw:'/abc/'}, 'slashes added');
eq(listValue(2,'regex',[],'  '), OFF, 'an empty expression is no filter');
eq(listValue(2,'words',['/buy|kupit/']), {flag:2,values:['/buy|kupit/'],raw:'/buy|kupit/'}, 'a typed /…/ is an expression');
eq(listValue(2,'words',['/x','y']), {flag:2,values:['/x','y'],raw:''}, 'raw never turns into an expression by accident');

// Expressions as typed, and the ones the engine could not use.
eq(rxText('  /a|b/i '), '/a|b/i', 'trimmed');
eq(rxText('abc'), '/abc/', 'slashes added');
eq(rxText(''), '', 'nothing typed');
eq(['/^Mozilla.*Android/i', '/a{2,3}/', 'plain words', '/\\d+ (x|y)/'].map(rxProblem), ['', '', '', ''], 'usable expressions');
eq(rxProblem('/a(b/'), 'not a valid expression', 'unbalanced group');
eq(rxProblem('/abc'), 'write it between slashes: /pattern/', 'no closing slash: the engine would never match');
eq(rxProblem('//i'), 'write it between slashes: /pattern/', 'empty pattern');
eq(rxProblem('/foo(?=bar)/'), 'lookarounds and back-references are not supported', 'lookahead: RE2 has none');
eq(rxProblem('/(?<!x)y/'), 'lookarounds and back-references are not supported', 'lookbehind');
eq(rxProblem('/(a)\\1/'), 'lookarounds and back-references are not supported', 'back-reference');

// The words a condition reads with.
eq(condOps('is','words'), ['is','is not'], 'exact lists');
eq(condOps('has','words'), ['contains','does not contain'], 'substrings');
eq(condOps('has','alt'), ['contains','does not contain'], 'substrings stored as an expression');
eq(condOps('dom','words'), ['is','is not'], 'domains are exact');
eq(condOps('dom','alt'), ['contains','does not contain'], 'a domain expression searches');
eq(condOps('has','regex'), ['matches','does not match'], 'an expression');

// Output variants.
eq(splitVariants('a|||b|||c'), ['a','b','c'], 'variants');
eq(splitVariants(' a ||| |||b '), ['a','b'], 'trimmed like the engine, empty dropped');
eq(splitVariants('one\ntwo '), ['one\ntwo '], 'a single output untouched');
eq(splitVariants(''), [], 'no output');
eq(splitVariants(null), [], 'no output (null)');
eq(joinVariants(['a','','b']), 'a|||b', 'empty variants dropped');
eq(joinVariants([' a ',' b']), 'a|||b', 'several are trimmed');
eq(joinVariants(['keep me ']), 'keep me ', 'one is kept as typed');
eq(joinVariants([]), '', 'none');
eq(joinVariants(splitVariants('x|||y|||z')), 'x|||y|||z', 'round trip');

// Page rewrites.
eq(rewriteRows('a|||b\nc|||d\r\nnoop\n'), [{find:'a',repl:'b'},{find:'c',repl:'d'}], 'pairs; lines without the separator skipped');
eq(rewriteRows('x|||y|||z'), [{find:'x',repl:'y|||z'}], 'split at the first separator, like the engine');
eq(joinRewrite([{find:'a',repl:'b'},{find:'',repl:'z'},{find:'c',repl:''}]), 'a|||b\nc|||', 'rows without find dropped');
eq(joinRewrite(rewriteRows('a|||b\nc|||d')), 'a|||b\nc|||d', 'round trip');

// Periods and days.
eq([60,3600,86400,604800,7200,1800,90,0,172800].map(humanSeconds),
  ['minute','hour','day','week','2 h','30 min','90 s','day','2 d'], 'periods');
eq(dayList([false,true,true,true,true,true,false]), 'weekdays', 'weekdays');
eq(dayList([true,false,false,false,false,false,true]), 'weekends', 'weekends');
eq(dayList([true,true,true,true,true,true,true]), 'every day', 'every day');
eq(dayList([false,false,false,false,false,false,false]), 'never', 'never');
eq(dayList([true,true,false,true,false,false,false]), 'Mon Wed Sun', 'Monday first');

// Macros.
eq(macroHTML('go [KEY] &amp; [PAR-1] [RANDNUM-1-9] [()COUNTRY()]'),
  'go <span class="mac">[KEY]</span> &amp; <span class="mac">[PAR-1]</span> <span class="mac">[RANDNUM-1-9]</span> <span class="mac">[()COUNTRY()]</span>', 'macros');
eq(macroHTML('[&quot;a&quot;] [lower] [1]'), '[&quot;a&quot;] [lower] [1]', 'not macros');

if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
