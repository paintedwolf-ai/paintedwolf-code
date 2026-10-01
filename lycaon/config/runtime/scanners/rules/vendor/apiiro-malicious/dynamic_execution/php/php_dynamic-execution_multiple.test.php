<?php

$code = 'echo "Hello";';
eval($code);

$func = create_function('', 'echo 1;');
$func();

assert(1 == 1, 'could be malicious');

$string = preg_replace('/e/', 'eval("echo 1")', 'test');
echo $string;

?>
