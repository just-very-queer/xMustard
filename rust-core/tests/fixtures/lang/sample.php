<?php
namespace App\Models;

use App\Contracts\Thing;
use Foo\Bar as Baz;
require_once 'boot.php';

// COMMENT_ONLY_WORD
/** Doc comment. */
class User extends Model implements Thing
{
    private $name;

    public function __construct($name)
    {
        $this->name = $name;
    }

    public function greet(string $who, int $n): string
    {
        $s = "STRING_ONLY_WORD";
        return helper($who) . $this->name;
    }

    private static function hidden() {}
}

interface Thing
{
    public function run();
}

function top($x)
{
    return strlen($x);
}
