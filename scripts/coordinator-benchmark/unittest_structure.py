"""Compare supplied Python test definitions without executing candidate code."""
import ast


def same_node(left, right):
    return ast.dump(left, include_attributes=False) == ast.dump(right, include_attributes=False)


def preserved_statements(original, candidate):
    cursor = 0
    for statement in original:
        if isinstance(statement, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
            definitions = [node for node in candidate if isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef))
                           and node.name == statement.name]
            if len(definitions) != 1:
                return False
        while cursor < len(candidate):
            current = candidate[cursor]
            cursor += 1
            if isinstance(statement, ast.ClassDef) and isinstance(current, ast.ClassDef):
                if statement.name != current.name:
                    continue
                original_header = {key: value for key, value in ast.iter_fields(statement) if key != 'body'}
                candidate_header = {key: value for key, value in ast.iter_fields(current) if key != 'body'}
                header_equal = same_node(ast.ClassDef(body=[], **original_header),
                                         ast.ClassDef(body=[], **candidate_header))
                if not header_equal or not preserved_statements(statement.body, current.body):
                    return False
                break
            if same_node(statement, current):
                break
        else:
            return False
    return True


def preserved_definitions(original, candidate):
    try:
        return preserved_statements(ast.parse(original).body, ast.parse(candidate).body)
    except (SyntaxError, ValueError, TypeError):
        return False
